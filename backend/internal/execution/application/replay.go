package executionapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Command-log replay runner v1 (v0.2.4-C EV-006, design §4.6; constitution
// art.4.4/4.6). Replay re-executes a source run's recorded command log under the
// SAME environment fingerprint + workspace snapshot and re-validates the
// assertions SERVER-side — there is no model run and no model self-report.
//
// Replay is a VERIFICATION, not a retest (replay ≠ retest): it never replaces the
// authoritative execution verdict. The replay anchor row carries the superseded
// process state so the coverage projection (LatestByCaseVersions filters
// status<>'superseded') and reports keep the source run as the case's result;
// the server-validated replay verdict and provenance live in summary.replay and
// a content-addressed replay-report evidence row. The four-state terminal write
// entry (writeTerminal) is untouched: replay writes no competing case_run
// terminal, it reuses the deterministic aggregation (execution.Aggregate) to
// compute its verification verdict.
//
// v1 boundaries (disclosed, not silent): replay covers a synchronous "exited"
// command log of one session; stdin is not part of the command record, nested
// workspace paths are not preserved by file-evidence settlement (files seed at
// their recorded basename), and absolute paths embedded in argv reference the
// source workspace. A log outside this boundary is rejected typed or degraded
// honestly, never silently replayed. Recording-grade replay is 0.3+.

// ReplayResult is the server-validated outcome of one command-log replay.
type ReplayResult struct {
	ReplayCaseRunID  int64                   `json:"replay_case_run_id"`
	SourceCaseRunID  int64                   `json:"source_case_run_id"`
	InputFingerprint string                  `json:"input_fingerprint"`
	Verdict          execution.CaseRunStatus `json:"verdict"`
	// SourceVerdict is the source run's authoritative terminal verdict; Consistent
	// reports whether the replay confirms it (faithful reproduction). A reproduced
	// failure confirms failed, never a guessed pass (constitution art.3.4/7.6).
	SourceVerdict  execution.CaseRunStatus `json:"source_verdict"`
	Consistent     bool                    `json:"consistent"`
	CommandTotal   int                     `json:"command_total"`
	CommandMatched int                     `json:"command_matched"`
	Commands       []ReplayCommandResult   `json:"commands"`
	Assertions     []ReplayAssertionResult `json:"assertions"`
}

// ReplayCommandResult is the server-side comparison of one recorded command
// against its replayed execution (exit + full stdio digests + outcome).
type ReplayCommandResult struct {
	CommandID    string `json:"command_id"`
	Matched      bool   `json:"matched"`
	RecordedExit *int   `json:"recorded_exit"`
	ReplayExit   *int   `json:"replay_exit"`
	Reason       string `json:"reason,omitempty"`
}

// ReplayAssertionResult is one server-re-validated assertion. Expected is the
// frozen case expected (server-owned, never the model's); the verdict is derived
// from the command-log reproduction, not copied from the source run's report.
type ReplayAssertionResult struct {
	StepSeq      int                        `json:"step_seq"`
	AssertionSeq int                        `json:"assertion_seq"`
	Verdict      execution.AssertionVerdict `json:"verdict"`
	Expected     string                     `json:"expected"`
	Actual       string                     `json:"actual"`
}

// recordedCommand is the decode of one command evidence record (the executor's
// command artifact JSON: argv/shell/cwd/env FULL/exit/outcome/stdio sha256).
type recordedCommand struct {
	CommandID       string            `json:"command_id"`
	Argv            []string          `json:"argv"`
	Shell           bool              `json:"shell"`
	Cwd             string            `json:"cwd"`
	Env             map[string]string `json:"env"`
	Exit            *int              `json:"exit"`
	Outcome         string            `json:"outcome"`
	StdoutSHA256    string            `json:"stdout_sha256"`
	StderrSHA256    string            `json:"stderr_sha256"`
	StdoutTruncated bool              `json:"stdout_truncated"`
	StderrTruncated bool              `json:"stderr_truncated"`
}

// replayEvidenceVersion is one evidence input folded into the replay input
// fingerprint (constitution art.4.6: the conclusion's evidence versions).
type replayEvidenceVersion struct {
	EvidenceID int64  `json:"evidence_id"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
}

type replayInputPayload struct {
	SourceCaseRunID        int64                   `json:"source_case_run_id"`
	CaseVersionID          int64                   `json:"case_version_id"`
	EnvironmentFingerprint string                  `json:"environment_fingerprint"`
	CodeBaseline           string                  `json:"code_baseline"`
	EvidenceVersions       []replayEvidenceVersion `json:"evidence_versions"`
}

// replayEvidencePageSize bounds one evidence page pull (the repo caps at 100).
const replayEvidencePageSize = 100

// runReplay is the replay use case body. The public entry (CaseRunner.ReplayCaseRun
// in case_run.go) validates configuration and delegates here.
func (s *CaseRunner) runReplay(ctx context.Context, versionID, sourceCaseRunID, actorID int64) (ReplayResult, error) {
	if s.caseRuns == nil || s.intents == nil || s.evidence == nil || s.content == nil ||
		s.execution == nil || s.versions == nil || s.cases == nil || s.sutContexts == nil || s.bindings == nil {
		return ReplayResult{}, errors.New("case runner is not configured for replay")
	}
	source, err := s.caseRuns.Get(ctx, sourceCaseRunID)
	if err != nil {
		return ReplayResult{}, mapExecutionError(err, "case_run")
	}
	intent, err := s.intents.Get(ctx, source.IntentID)
	if err != nil {
		return ReplayResult{}, mapExecutionError(err, "execution_intent")
	}
	if intent.VersionID != versionID {
		return ReplayResult{}, platform.NotFound("case_run")
	}
	if !source.Status.FourState() {
		return ReplayResult{}, &platform.Error{
			Status: 409, Code: "replay_not_terminal",
			Message: "replay requires a terminal four-state source run (passed/failed/blocked/inconclusive)",
		}
	}

	// Load the FULL evidence set of the source run in settlement (ascending ID)
	// order: the command log plus the workspace file snapshot.
	evidence, err := s.loadReplayEvidence(ctx, versionID, sourceCaseRunID)
	if err != nil {
		return ReplayResult{}, err
	}
	commands, files, err := s.decodeReplayLog(ctx, evidence)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(commands) == 0 {
		return ReplayResult{}, &platform.Error{
			Status: 409, Code: "replay_no_command_log",
			Message: "the source run recorded no command evidence; replay needs a command log (replay is not a retest)",
		}
	}
	for _, rec := range commands {
		if rec.Outcome != "exited" || len(rec.Argv) == 0 {
			return ReplayResult{}, &platform.Error{
				Status: 409, Code: "replay_unsupported_command_log",
				Message: fmt.Sprintf("command %q has outcome %q; replay v1 covers synchronous exited commands only", rec.CommandID, rec.Outcome),
			}
		}
	}

	// Replay input fingerprint folds the environment fingerprint, code baseline
	// AND the evidence versions the conclusion depends on (constitution art.4.6).
	inputFingerprint, err := replayInputFingerprint(sourceCaseRunID, source.CaseVersionID, intent, evidence)
	if err != nil {
		return ReplayResult{}, err
	}

	// Same-fingerprint gate: recompute the CURRENT environment fingerprint and
	// code baseline; a change rejects the replay typed (never a silent stale run).
	if err := s.requireReplayFingerprint(ctx, versionID, intent); err != nil {
		return ReplayResult{}, err
	}

	caseSnapshot, err := s.cases.GetCaseVersion(ctx, versionID, source.CaseVersionID)
	if err != nil {
		return ReplayResult{}, mapCaseReadError(err)
	}

	// Resolve the session identity BEFORE writing any anchor row: a multi-session
	// source (outside v1 scope) or a context read failure rejects typed here, so a
	// rejected replay leaves no orphan intent/case_run behind.
	sutID, role, err := s.replaySessionIdentity(ctx, source, intent)
	if err != nil {
		return ReplayResult{}, err
	}
	binding, err := s.bindingFor(ctx, versionID, sutID)
	if err != nil {
		return ReplayResult{}, err
	}

	// The source's server-recorded step/assertion results are the conclusion the
	// replay verifies consistency against (a reproduced failure confirms failed).
	sourceSteps, err := s.results.ListSteps(ctx, source.ID)
	if err != nil {
		return ReplayResult{}, mapExecutionError(err, "step_result")
	}
	sourceAssertions, err := s.results.ListAssertions(ctx, source.ID)
	if err != nil {
		return ReplayResult{}, mapExecutionError(err, "assertion_result")
	}

	// The replay anchor is a server-owned process row (superseded so it never
	// shadows the source verdict in coverage/reports). case_run is unique on
	// (wave_id, intent_id), so the anchor gets its OWN replay intent (purpose
	// manual — the purpose enum is frozen; a dedicated replay purpose would need a
	// migration) under the source wave. Both the intent and the case_run reach
	// their terminal through the state machine (create active -> guarded
	// transition), the same terminal-unique-entry discipline as the scheduler's
	// completeIntent (superseded case_run -> IntentSuperseded); neither is
	// born-terminal. Create and transition are ATOMIC (one transaction, T22):
	// a concurrent planner/scheduler tick can never observe the intermediate
	// open intent / pending anchor and claim it into an unintended retest wave,
	// and a failed transition leaves no orphan row behind.
	replayIntent := execution.ExecutionIntent{
		ID:                     s.nextID(),
		VersionID:              versionID,
		CaseVersionID:          source.CaseVersionID,
		SUTIDs:                 intent.SUTIDs,
		EnvironmentFingerprint: intent.EnvironmentFingerprint,
		CodeBaseline:           intent.CodeBaseline,
		Purpose:                execution.PurposeManual,
		Status:                 execution.IntentOpen,
		DedupeKey:              "replay:" + platformid.Format(sourceCaseRunID),
		AttemptNo:              1,
		CreatedAt:              s.now(),
		UpdatedAt:              s.now(),
	}
	if err := s.intents.CreateAndTransition(ctx, replayIntent, []execution.IntentStatus{execution.IntentOpen}, execution.IntentSuperseded); err != nil {
		return ReplayResult{}, mapExecutionError(err, "execution_intent")
	}
	anchor := execution.CaseRun{
		ID:            s.nextID(),
		WaveID:        source.WaveID,
		IntentID:      replayIntent.ID,
		CaseVersionID: source.CaseVersionID,
		Status:        execution.CaseRunPending,
		Attempts:      1,
		DispatchEpoch: 1,
		DispatchToken: "replay-" + platformid.Format(sourceCaseRunID),
		Summary: execution.CaseRunSummary{
			Replay: &execution.CaseRunReplay{
				SourceCaseRunID:        sourceCaseRunID,
				InputFingerprint:       inputFingerprint,
				EnvironmentFingerprint: intent.EnvironmentFingerprint,
				CodeBaseline:           intent.CodeBaseline,
				SourceVerdict:          string(source.Status),
			},
		},
		CreatedAt: s.now(),
		UpdatedAt: s.now(),
	}
	if err := s.caseRuns.CreateAndTransition(ctx, anchor, []execution.CaseRunStatus{execution.CaseRunPending}, execution.CaseRunSuperseded, anchor.DispatchEpoch, anchor.DispatchToken); err != nil {
		return ReplayResult{}, mapExecutionError(err, "case_run")
	}
	anchor.Status = execution.CaseRunSuperseded

	// Open a fresh replay session with its own workspace anchor.
	session, err := s.ensureSession(ctx, anchor, binding, role, sessionBaseURL(binding))
	if err != nil {
		return ReplayResult{}, err
	}
	defer s.closeSUTContexts(ctx, anchor.ID)

	// Seed the workspace snapshot: file evidence recorded BEFORE the first command
	// is the pre-execution input state. Files produced by commands are re-created
	// by the replay itself, so they are not pre-seeded (avoids corrupting append
	// semantics). v1 seeds at the recorded basename (settlement does not preserve
	// nested paths — disclosed fidelity boundary).
	if err := s.seedReplayWorkspace(ctx, anchor, session, commands[0].evidenceID, files); err != nil {
		return ReplayResult{}, err
	}

	// Re-execute the command log in order; compare each replayed command against
	// its record server-side (exit + full stdio digests + outcome).
	commandResults := make([]ReplayCommandResult, 0, len(commands))
	matched := 0
	for _, rec := range commands {
		result, execErr := s.replayOneCommand(ctx, anchor, session, rec)
		if execErr != nil {
			return ReplayResult{}, execErr
		}
		if result.Matched {
			matched++
		}
		commandResults = append(commandResults, result)
	}
	allMatched := matched == len(commands)

	// Server-side assertion re-validation (P2 unchanged): expected comes from the
	// frozen case snapshot. A faithful reproduction CONFIRMS the source's
	// server-recorded conclusion (a reproduced failure stays failed); a divergence
	// cannot confirm it (inconclusive). No model run, no guessed pass.
	assertionResults, stepResults := s.deriveReplayResults(anchor, caseSnapshot, allMatched, matched, len(commands), sourceSteps, sourceAssertions)
	for _, step := range stepResults {
		if ok, upErr := s.results.UpsertStep(ctx, step, anchor.DispatchEpoch, anchor.DispatchToken); upErr != nil {
			return ReplayResult{}, mapExecutionError(upErr, "step_result")
		} else if !ok {
			return ReplayResult{}, platform.LeaseLost()
		}
	}
	for _, assertion := range assertionResults {
		if ok, upErr := s.results.UpsertAssertion(ctx, assertion, anchor.DispatchEpoch, anchor.DispatchToken); upErr != nil {
			return ReplayResult{}, mapExecutionError(upErr, "assertion_result")
		} else if !ok {
			return ReplayResult{}, platform.LeaseLost()
		}
	}

	// Reuse the single deterministic settlement aggregation (execution.Aggregate)
	// to compute the replay verification verdict. Evidence drops during replay
	// degrade it to inconclusive (constitution art.4.5), exactly like a real run.
	status, _, summary, err := s.aggregateRunVerdict(ctx, anchor, caseSnapshot, stepResults, assertionResults,
		execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		return ReplayResult{}, err
	}
	consistent := allMatched && status == source.Status
	summary.Replay = anchor.Summary.Replay
	summary.Replay.Verdict = string(status)
	summary.Replay.SourceVerdict = string(source.Status)
	summary.Replay.Consistent = consistent
	summary.Replay.CommandTotal = len(commands)
	summary.Replay.CommandMatched = matched
	if !allMatched {
		summary.LastError = truncateRunes(fmt.Sprintf("replay diverged: %d/%d commands reproduced", matched, len(commands)), 2000)
	}
	anchor.Summary = summary
	anchor.UpdatedAt = s.now()
	if _, err := s.caseRuns.Update(ctx, anchor); err != nil {
		return ReplayResult{}, mapExecutionError(err, "case_run")
	}

	// Independent fact record (constitution art.5.7): a content-addressed
	// replay-report evidence row plus the append-only event and audit.
	replayAssertions := make([]ReplayAssertionResult, 0, len(assertionResults))
	for _, assertion := range assertionResults {
		replayAssertions = append(replayAssertions, ReplayAssertionResult{
			StepSeq: assertion.StepSeq, AssertionSeq: assertion.AssertionSeq,
			Verdict: assertion.Verdict, Expected: assertion.Expected, Actual: assertion.Actual,
		})
	}
	s.recordReplayReport(ctx, anchor, source, inputFingerprint, status, consistent, commandResults, replayAssertions)
	s.publishCaseRunEvent(EventCaseRunReplayed, anchor, map[string]any{
		"source_case_run_id": platformid.Format(sourceCaseRunID),
		"input_fingerprint":  inputFingerprint,
		"verdict":            string(status),
		"source_verdict":     string(source.Status),
		"consistent":         consistent,
		"command_total":      len(commands),
		"command_matched":    matched,
	})
	recordAudit(ctx, s.audit, "case_run.replayed", "case_run", anchor.ID, actorID, map[string]any{
		"source_case_run_id": platformid.Format(sourceCaseRunID),
		"verdict":            string(status),
		"consistent":         consistent,
	})

	return ReplayResult{
		ReplayCaseRunID:  anchor.ID,
		SourceCaseRunID:  sourceCaseRunID,
		InputFingerprint: inputFingerprint,
		Verdict:          status,
		SourceVerdict:    source.Status,
		Consistent:       consistent,
		CommandTotal:     len(commands),
		CommandMatched:   matched,
		Commands:         commandResults,
		Assertions:       replayAssertions,
	}, nil
}

// loadReplayEvidence pages the full evidence set of a case run (the repo caps a
// page at 100) and returns it in ascending settlement order.
func (s *CaseRunner) loadReplayEvidence(ctx context.Context, versionID, caseRunID int64) ([]execution.Evidence, error) {
	var all []execution.Evidence
	var beforeID *int64
	for {
		page, hasMore, err := s.evidence.ListByVersion(ctx, versionID, execution.EvidenceFilter{
			CaseRunID: &caseRunID, BeforeID: beforeID, Limit: replayEvidencePageSize,
		})
		if err != nil {
			return nil, mapExecutionError(err, "evidence")
		}
		all = append(all, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1].ID
		beforeID = &last
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

// replayCommand pairs a decoded command record with its evidence row id (used to
// order the workspace snapshot relative to the first command).
type replayCommand struct {
	recordedCommand
	evidenceID int64
}

// replayFile is one workspace file evidence row (content addressed by sha256).
type replayFile struct {
	evidenceID int64
	filename   string
	sha256     string
}

// decodeReplayLog splits the ascending evidence set into the command log and the
// workspace file snapshot. A command record that cannot be decoded fails closed
// (typed), never silently skipped.
func (s *CaseRunner) decodeReplayLog(ctx context.Context, evidence []execution.Evidence) ([]replayCommand, []replayFile, error) {
	var commands []replayCommand
	var files []replayFile
	for _, row := range evidence {
		switch row.Kind {
		case execution.EvidenceCommand:
			data, err := s.content.Open(ctx, row.SHA256)
			if err != nil {
				return nil, nil, mapExecutionError(err, "content")
			}
			var rec recordedCommand
			if err := json.Unmarshal(data, &rec); err != nil {
				return nil, nil, &platform.Error{
					Status: 409, Code: "replay_corrupt_command_log",
					Message: fmt.Sprintf("command evidence %s is not a decodable command record", platformid.Format(row.ID)),
				}
			}
			commands = append(commands, replayCommand{recordedCommand: rec, evidenceID: row.ID})
		case execution.EvidenceFile:
			files = append(files, replayFile{evidenceID: row.ID, filename: row.Filename, sha256: row.SHA256})
		}
	}
	return commands, files, nil
}

// replayInputFingerprint computes sha256 of the canonical replay input payload,
// folding the evidence versions (id+kind+sha256 of command/file evidence) so an
// evidence change makes a prior replay input stale (constitution art.4.6).
func replayInputFingerprint(sourceCaseRunID, caseVersionID int64, intent execution.ExecutionIntent, evidence []execution.Evidence) (string, error) {
	versions := make([]replayEvidenceVersion, 0, len(evidence))
	for _, row := range evidence {
		if row.Kind == execution.EvidenceCommand || row.Kind == execution.EvidenceFile {
			versions = append(versions, replayEvidenceVersion{
				EvidenceID: row.ID, Kind: string(row.Kind), SHA256: row.SHA256,
			})
		}
	}
	return hashCanonical(replayInputPayload{
		SourceCaseRunID:        sourceCaseRunID,
		CaseVersionID:          caseVersionID,
		EnvironmentFingerprint: intent.EnvironmentFingerprint,
		CodeBaseline:           intent.CodeBaseline,
		EvidenceVersions:       versions,
	})
}

// requireReplayFingerprint recomputes the current environment fingerprint and
// code baseline and rejects a change with a typed 409 (same-fingerprint gate).
//
// SC-F7: this gate keeps the LIVE read. It is the same-fingerprint replay safety
// (C SC-C9): replay must run under the environment the source run recorded, so the
// gate compares the recorded intent fingerprint against the CURRENT catalog.
// Switching it to the wave-frozen snapshot would make it a tautology (the frozen
// value always equals what the wave recorded) and silently allow replay into a
// drifted environment. An inability to recompute the current baseline (e.g. a
// repository whose commit is now unresolved) also rejects: sameness cannot be
// confirmed, so the replay fails closed.
func (s *CaseRunner) requireReplayFingerprint(ctx context.Context, versionID int64, intent execution.ExecutionIntent) error {
	binding, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return mapExecutionError(err, "version")
	}
	fingerprint, fpErr := EnvironmentFingerprint(binding)
	baseline, baselineErr := CodeBaseline(binding)
	if fpErr != nil || baselineErr != nil || fingerprint != intent.EnvironmentFingerprint || baseline != intent.CodeBaseline {
		return &platform.Error{
			Status: 409, Code: "replay_fingerprint_changed",
			Message: "the environment fingerprint or code baseline changed since the source run; replay requires the same fingerprint",
			Details: map[string]any{
				"recorded_environment_fingerprint": intent.EnvironmentFingerprint,
				"current_environment_fingerprint":  fingerprint,
			},
		}
	}
	return nil
}

// replaySessionIdentity resolves the (SUT, account_role) the source run's command
// log ran against. v1 replays ONE session: a source with more than one SUT context
// is outside scope and rejected typed (never silently replayed into one session's
// workspace/credentials). A context read failure fails closed rather than falling
// back to a guessed role.
func (s *CaseRunner) replaySessionIdentity(ctx context.Context, source execution.CaseRun, intent execution.ExecutionIntent) (int64, string, error) {
	rows, err := s.sutContexts.ListByCaseRun(ctx, source.ID)
	if err != nil {
		return 0, "", mapExecutionError(err, "case_run_sut_context")
	}
	if len(rows) > 1 {
		return 0, "", &platform.Error{
			Status: 409, Code: "replay_unsupported_command_log",
			Message: fmt.Sprintf("the source run used %d executor sessions; replay v1 covers a single session only", len(rows)),
		}
	}
	if len(rows) == 1 {
		return rows[0].SUTID, rows[0].AccountRole, nil
	}
	if len(intent.SUTIDs) > 0 {
		return intent.SUTIDs[0], "", nil
	}
	return 0, "", nil
}

// refreshReplaySession re-reads the durable session row so the settlement cursor
// reflects prior drains (storeExecutionEvidence advances the DB cursor, not the
// in-memory value). A read failure leaves the passed value unchanged.
func (s *CaseRunner) refreshReplaySession(ctx context.Context, anchor execution.CaseRun, session execution.CaseRunSUTContext) execution.CaseRunSUTContext {
	rows, err := s.sutContexts.ListByCaseRun(ctx, anchor.ID)
	if err != nil {
		return session
	}
	for _, row := range rows {
		if row.SessionID == session.SessionID {
			return row
		}
	}
	return session
}

// seedReplayWorkspace uploads the pre-command workspace snapshot (file evidence
// recorded before the first command) into the fresh replay session workspace.
func (s *CaseRunner) seedReplayWorkspace(ctx context.Context, anchor execution.CaseRun, session execution.CaseRunSUTContext, firstCommandEvidenceID int64, files []replayFile) error {
	for _, file := range files {
		if file.evidenceID >= firstCommandEvidenceID {
			// Produced during/after execution: the replay re-creates it.
			continue
		}
		if file.filename == "" {
			continue
		}
		data, err := s.content.Open(ctx, file.sha256)
		if err != nil {
			return mapExecutionError(err, "content")
		}
		if _, err := s.execution.Files(ctx, caseRunExecutorID(anchor), session.SessionID, FileOperation{
			Op: "upload", Path: file.filename, Base64: base64.StdEncoding.EncodeToString(data),
		}, session.LeaseEpoch); err != nil {
			return err
		}
		session = s.refreshReplaySession(ctx, anchor, session)
		if _, _, err := s.storeExecutionEvidence(ctx, anchor, session, 0); err != nil {
			return err
		}
	}
	return nil
}

// replayOneCommand re-executes one recorded command and compares the replayed
// record against the source server-side. The replayed full stdio digests come
// from the command evidence the executor settles for the replayed command (faithful
// even when stdio spilled), not the inline head.
func (s *CaseRunner) replayOneCommand(ctx context.Context, anchor execution.CaseRun, session execution.CaseRunSUTContext, rec replayCommand) (ReplayCommandResult, error) {
	// Refresh the settlement cursor from the durable row: storeExecutionEvidence
	// advances the DB cursor after each drain, but the in-memory session value is
	// stale, so reusing it would re-settle earlier commands (duplication).
	session = s.refreshReplaySession(ctx, anchor, session)
	outcome, execErr := s.execution.Exec(ctx, caseRunExecutorID(anchor), session.SessionID, ExecCommand{
		Argv: rec.Argv, Shell: rec.Shell, Env: rec.Env,
	}, session.LeaseEpoch)
	ids, _, drainErr := s.storeExecutionEvidence(ctx, anchor, session, 0)
	if execErr != nil && outcome.CommandID == "" {
		return ReplayCommandResult{}, execErr
	}
	if drainErr != nil {
		return ReplayCommandResult{}, drainErr
	}
	result := ReplayCommandResult{CommandID: rec.CommandID, RecordedExit: rec.Exit, ReplayExit: outcome.Exit}
	replayed, found := s.settledReplayCommand(ctx, ids)
	if !found {
		result.Reason = "the replayed command produced no command record"
		return result, nil
	}
	switch {
	case !exitEqual(rec.Exit, outcome.Exit):
		result.Reason = fmt.Sprintf("exit diverged: recorded %v, replayed %v", exitValue(rec.Exit), exitValue(outcome.Exit))
	case rec.StdoutSHA256 != replayed.StdoutSHA256:
		result.Reason = "stdout diverged (sha256 mismatch)"
	case rec.StderrSHA256 != replayed.StderrSHA256:
		result.Reason = "stderr diverged (sha256 mismatch)"
	case rec.Outcome != replayed.Outcome:
		result.Reason = fmt.Sprintf("outcome diverged: recorded %q, replayed %q", rec.Outcome, replayed.Outcome)
	default:
		result.Matched = true
	}
	return result, nil
}

// settledReplayCommand finds the command evidence record settled by one replay
// Exec call (the executor assigns a fresh command_id; the record is identified by
// kind among the settled evidence ids).
func (s *CaseRunner) settledReplayCommand(ctx context.Context, ids []string) (recordedCommand, bool) {
	for _, idText := range ids {
		id, err := platformid.Parse(idText)
		if err != nil {
			continue
		}
		row, err := s.evidence.Get(ctx, id)
		if err != nil || row.Kind != execution.EvidenceCommand {
			continue
		}
		data, err := s.content.Open(ctx, row.SHA256)
		if err != nil {
			continue
		}
		var rec recordedCommand
		if json.Unmarshal(data, &rec) == nil {
			return rec, true
		}
	}
	return recordedCommand{}, false
}

// deriveReplayResults builds the replay step/assertion results as CONSISTENCY
// WITH THE SOURCE CONCLUSION. Expected is always the frozen case expected
// (server-owned). A faithful command-log reproduction confirms the source's
// server-recorded step/assertion results (a reproduced failure stays failed —
// never a guessed pass, constitution art.3.4/7.6); a divergence cannot confirm the
// original observation, so the assertions are inconclusive and the deterministic
// aggregation settles inconclusive.
func (s *CaseRunner) deriveReplayResults(anchor execution.CaseRun, snapshot execution.CaseSnapshot, allMatched bool, matched, total int, sourceSteps []execution.StepResult, sourceAssertions []execution.AssertionResult) ([]execution.AssertionResult, []execution.StepResult) {
	now := s.now()
	sourceStepBySeq := make(map[int]execution.StepResult, len(sourceSteps))
	for _, result := range sourceSteps {
		sourceStepBySeq[result.Seq] = result
	}
	sourceAssertionByKey := make(map[[2]int]execution.AssertionResult, len(sourceAssertions))
	for _, result := range sourceAssertions {
		sourceAssertionByKey[[2]int{result.StepSeq, result.AssertionSeq}] = result
	}
	reproduced := fmt.Sprintf("command log reproduced (%d/%d commands matched); confirms the source observation", matched, total)
	diverged := fmt.Sprintf("command log diverged (%d/%d commands matched); replay cannot confirm the recorded observation", matched, total)

	assertions := make([]execution.AssertionResult, 0, len(snapshot.Assertions))
	for _, assertion := range snapshot.Assertions {
		verdict := execution.AssertionInconclusive
		actual := diverged
		if allMatched {
			actual = reproduced
			// Confirm the source's server-recorded conclusion for this assertion.
			if source, ok := sourceAssertionByKey[[2]int{assertion.StepSeq, assertion.AssertionSeq}]; ok {
				verdict = source.Verdict
			}
		}
		assertions = append(assertions, execution.AssertionResult{
			ID:           s.nextID(),
			CaseRunID:    anchor.ID,
			StepSeq:      assertion.StepSeq,
			AssertionSeq: assertion.AssertionSeq,
			Kind:         assertion.Kind,
			Verdict:      verdict,
			Expected:     truncateRunes(assertion.Expected, execution.MaxExpectedLength),
			Actual:       truncateRunes(actual, execution.MaxActualLength),
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	steps := make([]execution.StepResult, 0, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		status := execution.StepPassed
		actual := diverged
		if allMatched {
			actual = reproduced
			if source, ok := sourceStepBySeq[step.Seq]; ok {
				status = source.Status
			}
		}
		steps = append(steps, execution.StepResult{
			ID:        s.nextID(),
			CaseRunID: anchor.ID,
			Seq:       step.Seq,
			Status:    status,
			Observed:  map[string]any{"summary": actual},
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	return assertions, steps
}

// recordReplayReport persists the replay verification as a content-addressed
// text evidence row bound to the anchor (the independent fact record).
func (s *CaseRunner) recordReplayReport(ctx context.Context, anchor, source execution.CaseRun, inputFingerprint string, status execution.CaseRunStatus, consistent bool, commands []ReplayCommandResult, assertions []ReplayAssertionResult) {
	report := map[string]any{
		"source_case_run_id": platformid.Format(source.ID),
		"input_fingerprint":  inputFingerprint,
		"verdict":            string(status),
		"source_verdict":     string(source.Status),
		"consistent":         consistent,
		"commands":           commands,
		"assertions":         assertions,
	}
	data, err := json.Marshal(report)
	if err != nil {
		return
	}
	_, _ = s.addEvidenceForRun(ctx, anchor, 0, EvidenceInput{
		Kind: execution.EvidenceText, Data: data, Filename: "replay-report.json",
	})
}

func exitEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func exitValue(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
