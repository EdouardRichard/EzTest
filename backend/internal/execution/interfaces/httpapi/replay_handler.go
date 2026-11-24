package httpapi

import (
	"context"
	"net/http"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ReplayTrigger is the narrow command-log replay surface (v0.2.4-C EV-006).
// *executionapp.CaseRunner implements it; the handler stays testable without the
// full runner. A nil trigger answers 503 (degraded assembly), never a panic.
type ReplayTrigger interface {
	ReplayCaseRun(ctx context.Context, versionID, sourceCaseRunID, actorID int64) (executionapp.ReplayResult, error)
}

// replayResultView is the REST projection of one server-validated replay. IDs
// are decimal strings; the verdict and per-command/per-assertion outcomes are the
// server's deterministic comparison, never a model self-report.
type replayResultView struct {
	ReplayCaseRunID  string                `json:"replay_case_run_id"`
	SourceCaseRunID  string                `json:"source_case_run_id"`
	InputFingerprint string                `json:"input_fingerprint"`
	Verdict          string                `json:"verdict"`
	SourceVerdict    string                `json:"source_verdict"`
	Consistent       bool                  `json:"consistent"`
	CommandTotal     int                   `json:"command_total"`
	CommandMatched   int                   `json:"command_matched"`
	Commands         []replayCommandView   `json:"commands"`
	Assertions       []replayAssertionView `json:"assertions"`
}

type replayCommandView struct {
	CommandID    string `json:"command_id"`
	Matched      bool   `json:"matched"`
	RecordedExit *int   `json:"recorded_exit"`
	ReplayExit   *int   `json:"replay_exit"`
	Reason       string `json:"reason,omitempty"`
}

type replayAssertionView struct {
	StepSeq      int    `json:"step_seq"`
	AssertionSeq int    `json:"assertion_seq"`
	Verdict      string `json:"verdict"`
	Expected     string `json:"expected"`
	Actual       string `json:"actual"`
}

// ReplayCaseRun POST /api/versions/{id}/case-runs/{caseRunId}/replay.
//
// Triggers a server-side command-log replay of a terminal case_run under the same
// environment fingerprint + workspace snapshot. The assertions are re-validated
// server-side (P2 unchanged); replay is a verification, not a retest. A changed
// fingerprint, a missing command log or an unsupported log is a typed 409.
func (h *Handler) ReplayCaseRun(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := pathParamID(r, "caseRunId", "case_run_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Replay == nil {
		httpx.Error(w, r, platform.Unavailable("command-log replay is not configured"))
		return
	}
	result, err := h.deps.Replay.ReplayCaseRun(ctx, versionID, caseRunID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReplayResultView(result))
}

func toReplayResultView(result executionapp.ReplayResult) replayResultView {
	view := replayResultView{
		ReplayCaseRunID:  platformid.Format(result.ReplayCaseRunID),
		SourceCaseRunID:  platformid.Format(result.SourceCaseRunID),
		InputFingerprint: result.InputFingerprint,
		Verdict:          string(result.Verdict),
		SourceVerdict:    string(result.SourceVerdict),
		Consistent:       result.Consistent,
		CommandTotal:     result.CommandTotal,
		CommandMatched:   result.CommandMatched,
		Commands:         make([]replayCommandView, 0, len(result.Commands)),
		Assertions:       make([]replayAssertionView, 0, len(result.Assertions)),
	}
	for _, command := range result.Commands {
		view.Commands = append(view.Commands, replayCommandView{
			CommandID: command.CommandID, Matched: command.Matched,
			RecordedExit: command.RecordedExit, ReplayExit: command.ReplayExit, Reason: command.Reason,
		})
	}
	for _, assertion := range result.Assertions {
		view.Assertions = append(view.Assertions, replayAssertionView{
			StepSeq: assertion.StepSeq, AssertionSeq: assertion.AssertionSeq,
			Verdict: string(assertion.Verdict), Expected: assertion.Expected, Actual: assertion.Actual,
		})
	}
	return view
}
