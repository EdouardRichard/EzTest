package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// --- execution tools -------------------------------------------------------

// SUT-selector wire contract (v0.2.4-C T14, R-C1): the SUT-selector wire rename
// (the stale version-SUT name -> sut_id) moved across ALL SIX model-facing
// surfaces in one lockstep change. B's lesson: renaming one side only makes the
// model's argument decode to zero — "错而绿" — so every surface moved together:
//
//  1. Tool ARGUMENT names — agent/application/tools/case_execution_tools.go Parameters()
//     schemas, the arg decode json tags and the parseIDString error-field
//     literals all use sut_id. The execution-side decode structs mirrored here
//     (httpToolArgs, dbQueryToolArgs, schemaReadToolArgs) use the same sut_id tag,
//     so the re-marshaled agent arguments decode correctly.
//  2. Tool RESULT keys — sut_id in the account.credentials (credentials.go),
//     database.schema_read (below) and evidence.read (evidence.go) results;
//     sut_ids in code.impact (code_tools.go, U28); suts/sut_id/target_sut_id in
//     the context.task_pack execution snapshot (runner.go executionTaskResult).
//  3. ERROR FIELD names — platform.Invalid("sut_id", ...) in credentials.go
//     (resolveSUTID x2, bindingFor) and case_run.go (assertion target mismatch).
//     platform/errors.go puts the string in Details{"field":...} and the registry
//     copies it verbatim, so the model is told to fix the argument it actually
//     sent (sut_id), not a name absent from its schema (which DisallowUnknownFields
//     would hard-reject).
//  4. PROMPT rendering — context.go executionBlock labels the selector sut=.
//  5. VALUE SOURCE carry — the task_pack execution.suts[] entries CARRY sut_id
//     (the value the prompt names), not merely exist as a readable tool.
//  6. MODEL STAND-IN — the scripts/fake-llm task_pack fixtures use the same keys.
//
// NOT part of this contract, and correctly rekeyed to sut_id earlier (B): the
// HTTP API (interfaces/httpapi views + the ?sut_id= query param), the DB columns,
// the executor session metadata, audit metadata, log fields, and the internal
// "sut_ids" error field of errors.go/fingerprint.go (intent creation is an HTTP
// path, never a tool result).
//
// The transitional browser bridge (browserToolArgs / CaseRunner.Browser) retired
// in T16; the browser domain is the official playwright-cli over Exec (T15).

// ExecCommandInput is one general-execution call: which (SUT, account_role)
// session runs the command, and the command itself. The model-facing tool schema
// that binds to it is registered by the capability-domain tool surface (T11).
type ExecCommandInput struct {
	SUTID       *int64
	AccountRole string
	Command     ExecCommand
}

// Exec runs one command in the case_run's executor session and settles its
// recording as immutable evidence. It is the general execution path (constitution
// art.2.6 escape valve): argv by default, an explicit shell when asked, and every
// command/file/trace artifact it produces lands in the content ledger.
func (s *CaseRunner) Exec(ctx context.Context, scope tools.Scope, in ExecCommandInput) (ExecOutcome, error) {
	if s.execution == nil {
		return ExecOutcome{}, errors.New("case runner has no execution executor")
	}
	if len(in.Command.Argv) == 0 {
		return ExecOutcome{}, platform.Invalid("argv", "argv must not be empty")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return ExecOutcome{}, err
	}
	sutID, err := s.resolveSUTID(ctx, scope, in.SUTID)
	if err != nil {
		return ExecOutcome{}, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, sutID)
	if err != nil {
		return ExecOutcome{}, err
	}
	session, err := s.ensureSession(ctx, caseRun, binding, in.AccountRole, sessionBaseURL(binding))
	if err != nil {
		return ExecOutcome{}, err
	}
	outcome, execErr := s.execution.Exec(ctx, caseRunExecutorID(caseRun), session.SessionID, in.Command, session.LeaseEpoch)
	outcome.SessionID = session.SessionID
	if execErr != nil && sessionGone(execErr) {
		// The session was reclaimed, crashed or expired: its id is dead. Release
		// the reservation so the NEXT call creates a fresh session instead of
		// failing identically for the rest of the run (the browser path has the
		// same recovery). A stale-epoch lease_lost is deliberately NOT included:
		// there a newer owner holds the session, and re-creating it would let this
		// stale dispatch steal work it no longer owns.
		s.markSUTContext(ctx, caseRun.ID, sutID, in.AccountRole, execution.SUTContextFailed)
	}
	// Settle even when the command failed or timed out: the record of what
	// actually ran is evidence, not an optional extra (constitution art.4.5).
	ids, skipped, drainErr := s.storeExecutionEvidence(ctx, caseRun, session, scope.AgentRunID)
	outcome.EvidenceIDs = ids
	outcome.EvidenceSkipped = skipped
	if execErr != nil {
		return outcome, execErr
	}
	if drainErr != nil {
		return outcome, drainErr
	}
	return outcome, nil
}

// ExecRun is the model-facing exec.run wrapper of the Exec use case: it maps the
// tool DTO onto the typed command and renders the settled outcome. A command
// that RAN and was recorded (even a timeout) is data, not a tool error — the
// model needs the recorded stdout/exit to diagnose; only a call that never
// produced a command record (scope fence, dead session, transport) errors.
func (s *CaseRunner) ExecRun(ctx context.Context, scope tools.Scope, in tools.ExecRunInput) (any, error) {
	outcome, err := s.Exec(ctx, scope, ExecCommandInput{
		SUTID:       in.SUTID,
		AccountRole: in.AccountRole,
		Command: ExecCommand{
			Argv:       in.Argv,
			Shell:      in.Shell,
			Stdin:      in.Stdin,
			TimeoutMS:  in.TimeoutMS,
			Background: in.Background,
			Env:        in.Env,
		},
	})
	if err != nil && outcome.CommandID == "" {
		return nil, err
	}
	out := map[string]any{
		"command_id":       outcome.CommandID,
		"exit":             outcome.Exit,
		"outcome":          outcome.Outcome,
		"duration_ms":      outcome.DurationMS,
		"stdout":           outcome.Stdout,
		"stderr":           outcome.Stderr,
		"stdout_truncated": outcome.StdoutTruncated,
		"stderr_truncated": outcome.StderrTruncated,
		"background":       outcome.Background,
		"timed_out":        outcome.TimedOut,
		"cancelled":        outcome.Cancelled,
	}
	if outcome.StdoutRef != "" {
		out["stdout_ref"] = outcome.StdoutRef
	}
	if outcome.StderrRef != "" {
		out["stderr_ref"] = outcome.StderrRef
	}
	appendEvidenceOutcome(out, outcome.EvidenceIDs, outcome.EvidenceSkipped)
	return out, nil
}

// ShellOp is the server use case behind the shell.* and pty.* tool families.
// Both families drive the executor's ONE PTY shell channel inside the same
// lazily created, dispatch-fenced (SUT, account_role) session as exec; every op
// drains the recording ledger afterwards (a shell settles one command artifact
// on exit, and the workspace watcher settles file changes).
func (s *CaseRunner) ShellOp(ctx context.Context, scope tools.Scope, in tools.ShellOpInput) (any, error) {
	if s.execution == nil {
		return nil, errors.New("case runner has no execution executor")
	}
	switch in.Op {
	case "create", "write", "read", "close", "wait":
	default:
		return nil, platform.Invalid("op", "unsupported shell operation")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	sutID, err := s.resolveSUTID(ctx, scope, in.SUTID)
	if err != nil {
		return nil, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, err
	}
	session, err := s.ensureSession(ctx, caseRun, binding, in.AccountRole, sessionBaseURL(binding))
	if err != nil {
		return nil, err
	}
	outcome, opErr := s.execution.Shell(ctx, caseRunExecutorID(caseRun), session.SessionID, ShellOperation{
		Op:        in.Op,
		ShellID:   in.ShellID,
		Argv:      in.Argv,
		Cols:      in.Cols,
		Rows:      in.Rows,
		Env:       in.Env,
		Data:      in.Data,
		Since:     in.Since,
		TimeoutMS: in.TimeoutMS,
	}, session.LeaseEpoch)
	if opErr != nil && sessionGone(opErr) {
		// Same recovery as Exec: a dead session id releases the reservation so
		// the next call re-creates a session; lease_lost deliberately does not.
		s.markSUTContext(ctx, caseRun.ID, sutID, in.AccountRole, execution.SUTContextFailed)
	}
	ids, skipped, drainErr := s.storeExecutionEvidence(ctx, caseRun, session, scope.AgentRunID)
	if opErr != nil {
		return nil, opErr
	}
	if drainErr != nil {
		return nil, drainErr
	}
	out := map[string]any{"op": in.Op}
	if outcome.ShellID != "" {
		out["shell_id"] = outcome.ShellID
	}
	if outcome.PID > 0 {
		out["pid"] = outcome.PID
	}
	if outcome.Status != "" {
		out["status"] = outcome.Status
	}
	if in.Op == "read" {
		out["data"] = outcome.Data
		out["next_cursor"] = outcome.NextCursor
		out["total_bytes"] = outcome.TotalBytes
		out["truncated"] = outcome.Truncated
	}
	if outcome.Exit != nil {
		out["exit"] = outcome.Exit
	}
	if outcome.Outcome != "" {
		out["outcome"] = outcome.Outcome
	}
	if in.Op == "close" || in.Op == "wait" {
		out["ok"] = outcome.OK
	}
	appendEvidenceOutcome(out, ids, skipped)
	return out, nil
}

// FileOp is the server use case behind the file.* tool family: workspace
// upload/download/list inside the dispatch-fenced session. Uploads and the
// workspace watcher's file artifacts settle as immutable file evidence.
func (s *CaseRunner) FileOp(ctx context.Context, scope tools.Scope, in tools.FileOpInput) (any, error) {
	if s.execution == nil {
		return nil, errors.New("case runner has no execution executor")
	}
	switch in.Op {
	case "upload", "download", "list":
	default:
		return nil, platform.Invalid("op", "unsupported file operation")
	}
	if in.Op != "list" && strings.TrimSpace(in.Path) == "" {
		return nil, platform.Invalid("path", "path is required")
	}
	payload := ""
	if in.Op == "upload" {
		switch in.Encoding {
		case "", "text":
			payload = base64.StdEncoding.EncodeToString([]byte(in.Content))
		case "base64":
			if _, err := base64.StdEncoding.DecodeString(in.Content); err != nil {
				return nil, platform.Invalid("content", "must be valid base64 when encoding is base64")
			}
			payload = in.Content
		default:
			return nil, platform.Invalid("encoding", "encoding must be text or base64")
		}
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	sutID, err := s.resolveSUTID(ctx, scope, in.SUTID)
	if err != nil {
		return nil, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, err
	}
	session, err := s.ensureSession(ctx, caseRun, binding, in.AccountRole, sessionBaseURL(binding))
	if err != nil {
		return nil, err
	}
	outcome, opErr := s.execution.Files(ctx, caseRunExecutorID(caseRun), session.SessionID, FileOperation{
		Op:     in.Op,
		Path:   in.Path,
		Base64: payload,
	}, session.LeaseEpoch)
	if opErr != nil && sessionGone(opErr) {
		s.markSUTContext(ctx, caseRun.ID, sutID, in.AccountRole, execution.SUTContextFailed)
	}
	ids, skipped, drainErr := s.storeExecutionEvidence(ctx, caseRun, session, scope.AgentRunID)
	if opErr != nil {
		return nil, opErr
	}
	if drainErr != nil {
		return nil, drainErr
	}
	out := map[string]any{"op": in.Op}
	switch in.Op {
	case "upload":
		out["path"] = outcome.Path
		out["bytes"] = outcome.Bytes
		out["sha256"] = outcome.SHA256
		out["mime"] = outcome.MIME
	case "download":
		out["path"] = outcome.Path
		out["bytes"] = outcome.Bytes
		out["sha256"] = outcome.SHA256
		out["mime"] = outcome.MIME
		out["truncated"] = outcome.Truncated
		data, decodeErr := base64.StdEncoding.DecodeString(outcome.Base64)
		if decodeErr != nil {
			return nil, errors.New("executor file content is not valid base64")
		}
		// Explicit encoding, never a lossy silent conversion: valid UTF-8 is
		// returned as text, anything else stays base64.
		if utf8.Valid(data) {
			out["content"] = string(data)
			out["encoding"] = "text"
		} else {
			out["content"] = outcome.Base64
			out["encoding"] = "base64"
		}
	case "list":
		files := make([]map[string]any, 0, len(outcome.Files))
		for _, entry := range outcome.Files {
			files = append(files, map[string]any{
				"path":  entry.Path,
				"bytes": entry.Bytes,
				"mime":  entry.MIME,
				"dir":   entry.Dir,
			})
		}
		out["files"] = files
		out["truncated"] = outcome.Truncated
	}
	appendEvidenceOutcome(out, ids, skipped)
	return out, nil
}

// appendEvidenceOutcome renders the UNIFIED evidence settlement signal of the
// general-execution tool results: evidence_skipped is always the dropped COUNT
// (never a bool), with an explicit reason. The per-drop causes are in the
// append-only evidence.skipped events; the persisted drop fact degrades the
// run's verdict (constitution art.4.5 / art.7.6).
func appendEvidenceOutcome(out map[string]any, ids []string, skipped int) {
	if len(ids) > 0 {
		out["evidence_ids"] = ids
	}
	if skipped > 0 {
		out["evidence_skipped"] = skipped
		out["evidence_skipped_reason"] = "evidence_dropped"
	}
}

// sessionGone reports whether a typed executor error means the session itself is
// no longer usable by this owner, so its reservation must be released and the
// next call re-creates a session.
//
// The codes mirror infrastructure/executor CodeSessionNotFound / CodeLeaseExpired
// / CodeSessionReclaiming (the application layer must not import infrastructure,
// so the literals are duplicated here and MUST stay in lockstep with the client
// constants — the same pattern as classifyExecutorError's executor_timeout /
// executor_unavailable literals).
func sessionGone(err error) bool {
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "session_not_found", "lease_expired", "session_reclaiming":
		return true
	default:
		return false
	}
}

// caseRunExecutorID returns the owning executor id (0 = single-endpoint
// fallback when the run has no owner, e.g. a historical row).
func caseRunExecutorID(caseRun execution.CaseRun) int64 {
	if caseRun.OwnerExecutorID == nil {
		return 0
	}
	return *caseRun.OwnerExecutorID
}

// sessionBaseURL is the informational base URL hint of a session (the retained
// browser bridge resolves relative navigation against it).
func sessionBaseURL(binding execution.SUTBinding) string {
	if binding.BaseURL != "" {
		return binding.BaseURL
	}
	return defaultBaseURL(binding)
}

// ensureSession returns the active executor v2 session of one (SUT, account_role)
// binding, creating it lazily. Each binding owns its own session so concurrent
// account roles never share a browser context or a workspace watcher; the
// workspace is anchored under the case_run's own directory.
func (s *CaseRunner) ensureSession(ctx context.Context, caseRun execution.CaseRun, binding execution.SUTBinding, role, baseURL string) (execution.CaseRunSUTContext, error) {
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRunSUTContext{}, mapExecutionError(err, "case_run_sut_context")
	}
	var row *execution.CaseRunSUTContext
	for i := range rows {
		if rows[i].SUTID == binding.SUTID && rows[i].AccountRole == role {
			row = &rows[i]
			break
		}
	}
	if row != nil && row.SessionID != "" && row.Status == execution.SUTContextActive {
		if row.LeaseEpoch == caseRun.DispatchEpoch {
			return *row, nil
		}
		// A newer dispatch re-claimed this run: adopt its epoch on the existing
		// session so the executor's fencing accepts the new owner's writes (the
		// executor adopts a higher epoch, exactly like a dispatch re-claim).
		// Without this the reused session would carry the previous owner's epoch
		// and every write would be fenced with 409 lease_lost.
		rowID := row.ID
		ok, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
			target.LeaseEpoch = caseRun.DispatchEpoch
			target.ExecutorID = caseRun.OwnerExecutorID
			return nil
		})
		if updateErr != nil {
			return execution.CaseRunSUTContext{}, mapExecutionError(updateErr, "case_run_sut_context")
		}
		if !ok {
			return execution.CaseRunSUTContext{}, platform.LeaseLost()
		}
		row.LeaseEpoch = caseRun.DispatchEpoch
		row.ExecutorID = caseRun.OwnerExecutorID
		return *row, nil
	}
	workspace, err := s.sessionWorkspace(ctx, caseRun.ID, binding.SUTID, role)
	if err != nil {
		return execution.CaseRunSUTContext{}, err
	}
	ttl := time.Duration(s.intSetting("execution.executor_context_ttl_seconds", 300)) * time.Second
	spec := SessionSpec{
		Key:        caseSessionKey(caseRun.ID, binding.SUTID, role),
		Workspace:  workspace,
		BaseURL:    baseURL,
		ExecutorID: caseRunExecutorID(caseRun),
		LeaseEpoch: caseRun.DispatchEpoch,
		LeaseTTL:   ttl,
		Metadata: map[string]any{
			"case_run_id":    platformid.Format(caseRun.ID),
			"attempt":        caseRun.Attempts,
			"sut_id":         platformid.Format(binding.SUTID),
			"account_role":   role,
			"dispatch_epoch": platformid.Format(caseRun.DispatchEpoch),
			"dispatch_token": caseRun.DispatchToken,
		},
	}
	sessionID, err := s.execution.CreateSession(ctx, spec)
	if err != nil {
		s.markSUTContext(ctx, caseRun.ID, binding.SUTID, role, execution.SUTContextFailed)
		return execution.CaseRunSUTContext{}, err
	}
	if row == nil {
		created := execution.CaseRunSUTContext{
			ID:          s.nextID(),
			CaseRunID:   caseRun.ID,
			SUTID:       binding.SUTID,
			AccountRole: role,
			SessionID:   sessionID,
			ExecutorID:  caseRun.OwnerExecutorID,
			LeaseEpoch:  caseRun.DispatchEpoch,
			Status:      execution.SUTContextActive,
			CreatedAt:   s.now(),
			UpdatedAt:   s.now(),
		}
		if createErr := s.sutContexts.Create(ctx, created); createErr != nil {
			_ = s.execution.Close(ctx, caseRunExecutorID(caseRun), sessionID)
			return execution.CaseRunSUTContext{}, mapExecutionError(createErr, "case_run_sut_context")
		}
		return created, nil
	}
	rowID := row.ID
	ok, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
		target.SessionID = sessionID
		target.ExecutorID = caseRun.OwnerExecutorID
		target.Status = execution.SUTContextActive
		target.LeaseEpoch = caseRun.DispatchEpoch
		// A new session starts a new recording ledger.
		target.ArtifactCursor = 0
		return nil
	})
	if updateErr != nil {
		return execution.CaseRunSUTContext{}, mapExecutionError(updateErr, "case_run_sut_context")
	}
	if !ok {
		return execution.CaseRunSUTContext{}, platform.LeaseLost()
	}
	row.SessionID = sessionID
	row.ExecutorID = caseRun.OwnerExecutorID
	row.Status = execution.SUTContextActive
	row.LeaseEpoch = caseRun.DispatchEpoch
	row.ArtifactCursor = 0
	return *row, nil
}

// sessionWorkspace is the absolute workspace anchor of one execution session:
// <paths.data_root>/workspaces/run-<case_run_id>/sut-<sut_id>-<role>. The API and
// the executor run on the same host in the single-admin trusted deployment, so
// the API resolves the path absolutely before handing it over.
func (s *CaseRunner) sessionWorkspace(ctx context.Context, caseRunID, sutID int64, role string) (string, error) {
	root := "./.data"
	if s.settings != nil {
		if value, err := s.settings.String(ctx, "paths.data_root"); err == nil && strings.TrimSpace(value) != "" {
			root = strings.TrimSpace(value)
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return filepath.Join(absolute, "workspaces",
		fmt.Sprintf("run-%d", caseRunID),
		fmt.Sprintf("sut-%d-%s", sutID, workspaceRoleSegment(role))), nil
}

// workspaceRoleSegment keeps an account role usable as one path segment.
func workspaceRoleSegment(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "default"
	}
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, trimmed)
	if len(mapped) > 64 {
		mapped = mapped[:64]
	}
	return mapped
}

func (s *CaseRunner) markSUTContext(ctx context.Context, caseRunID, sutID int64, role string, status execution.SUTContextStatus) {
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.SUTID != sutID || row.AccountRole != role {
			continue
		}
		rowID := row.ID
		_, _ = s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
			target.Status = status
			target.SessionID = ""
			return nil
		})
	}
}

// storeExecutionEvidence pulls the session's recording ledger from the persisted
// cursor and settles every new artifact as evidence, then persists the advanced
// cursor. It is the producer that closes the recording chain end to end:
// executor artifact → client → content ledger → evidence row.
//
// Nothing is silently lost (constitution art.4.5 / art.7.6): a cursor gap, an
// artifact whose content the executor could not transfer, a failed recording and
// a FAILED SETTLEMENT (unreachable ledger, bad base64, digest mismatch, content
// store error) are each counted as an evidence drop, which degrades the run to
// inconclusive. On a mid-page failure the whole un-settled remainder is counted
// and the cursor is left where it was so the remainder stays retryable.
func (s *CaseRunner) storeExecutionEvidence(ctx context.Context, caseRun execution.CaseRun, session execution.CaseRunSUTContext, agentRunID int64) ([]string, int, error) {
	if s.execution == nil || session.SessionID == "" || s.evidence == nil || s.content == nil {
		return nil, 0, nil
	}
	executorID := int64(0)
	if session.ExecutorID != nil {
		executorID = *session.ExecutorID
	}
	// Ask the executor to settle its trace file first: the trace route ledger
	// entry is produced on read, so without this call a recorded trace would
	// never become evidence (the trace kind's producer, constitution art.4.5).
	if _, traceErr := s.execution.Trace(ctx, executorID, session.SessionID); traceErr != nil {
		slog.WarnContext(ctx, "executor trace probe failed",
			"case_run_id", caseRun.ID, "session_id", session.SessionID, "error", traceErr)
	}
	page, err := s.execution.Artifacts(ctx, executorID, session.SessionID, session.ArtifactCursor)
	if err != nil {
		// The ledger could not be read at all: its size is unknown, so record one
		// explicit loss rather than pretending nothing was missed.
		s.recordSettlementFailure(ctx, caseRun, session, 1, err)
		return nil, 1, err
	}
	ids := make([]string, 0, len(page.Artifacts))
	skipped := 0
	// processed counts the artifacts fully handled (settled, or counted as a
	// per-artifact drop). On a failure at index i the remainder len-i never
	// settled and must be counted as lost.
	processed := 0
	fail := func(cause error) ([]string, int, error) {
		remaining := int64(len(page.Artifacts) - processed)
		if remaining < 1 {
			remaining = 1
		}
		s.recordSettlementFailure(ctx, caseRun, session, remaining, cause)
		return ids, skipped + int(remaining), cause
	}
	if page.Gap && page.Missed > 0 {
		// The settlement cursor fell behind the executor's bounded buffer: the
		// evicted artifacts were spilled to the workspace but are no longer in
		// the pull window. Count them explicitly instead of pretending the
		// recording is complete.
		s.recordEvidenceDrops(ctx, caseRun, page.Missed, "artifact_cursor_gap", execution.EvidenceKind(""), nil)
		skipped += int(page.Missed)
	}
	if page.EvictionFailed > 0 {
		slog.WarnContext(ctx, "executor kept evicted artifacts in memory after a failed spill",
			"case_run_id", caseRun.ID, "session_id", session.SessionID,
			"artifact_eviction_failed", page.EvictionFailed)
	}
	for index, artifact := range page.Artifacts {
		processed = index
		kind := execution.EvidenceKind(artifact.Kind)
		if !kind.Valid() {
			// Fail closed for this artifact: it is never stored, and the loss is
			// counted + evented so the verdict degrades (spec SC-C3 allows
			// explicit counted degradation in place of a hard error).
			slog.ErrorContext(ctx, "executor emitted an undeclared artifact kind",
				"case_run_id", caseRun.ID, "session_id", session.SessionID, "kind", artifact.Kind)
			s.recordEvidenceDrops(ctx, caseRun, 1, "undeclared_artifact_kind", execution.EvidenceKind(""), nil)
			skipped++
			continue
		}
		if artifact.RecordingFailed {
			slog.ErrorContext(ctx, "executor could not record a command",
				"case_run_id", caseRun.ID, "session_id", session.SessionID,
				"command_id", artifact.CommandID, "error", artifact.Error)
			s.recordEvidenceDrops(ctx, caseRun, 1, "recording_failed", kind, nil)
			skipped++
			continue
		}
		if artifact.ContentOmitted || artifact.Base64 == "" {
			slog.WarnContext(ctx, "executor artifact content was not transferable",
				"case_run_id", caseRun.ID, "session_id", session.SessionID,
				"kind", artifact.Kind, "path", artifact.Path, "reason", artifact.ContentReason)
			s.recordEvidenceDrops(ctx, caseRun, 1, "artifact_content_omitted", kind, nil)
			skipped++
			continue
		}
		data, decodeErr := base64.StdEncoding.DecodeString(artifact.Base64)
		if decodeErr != nil {
			return fail(errors.New("executor artifact is not valid base64"))
		}
		// Content addressing is verified, not trusted: the executor's digest must
		// match the bytes the API actually received.
		if artifact.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != artifact.SHA256 {
				return fail(fmt.Errorf("executor artifact %q sha256 mismatch", artifact.Path))
			}
		}
		sutID := session.SUTID
		evidence, addErr := s.addEvidenceForRun(ctx, caseRun, agentRunID, EvidenceInput{
			Kind: kind, Data: data, Filename: artifact.Filename, SUTID: &sutID, Auto: true,
		})
		if addErr != nil {
			return fail(addErr)
		}
		if evidence.ID > 0 {
			ids = append(ids, platformid.Format(evidence.ID))
		} else {
			skipped++
		}
	}
	processed = len(page.Artifacts)
	if page.NextCursor > session.ArtifactCursor {
		rowID := session.ID
		cursor := page.NextCursor
		if rowID > 0 && s.sutContexts != nil {
			if _, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
				if cursor > target.ArtifactCursor {
					target.ArtifactCursor = cursor
				}
				return nil
			}); updateErr != nil {
				// Every artifact of this page IS stored; only the watermark failed to
				// persist, so the next drain may re-settle them. That is duplication,
				// not loss — it must not degrade the verdict, but it must be loud.
				slog.ErrorContext(ctx, "execution evidence cursor could not be persisted; the next drain may re-settle this page",
					"case_run_id", caseRun.ID, "session_id", session.SessionID,
					"artifact_cursor", cursor, "error", updateErr)
				return ids, skipped, mapExecutionError(updateErr, "case_run_sut_context")
			}
		}
	}
	return ids, skipped, nil
}

// recordSettlementFailure counts evidence that a FAILED settlement left behind
// and logs it loudly. The caller's budget may already be exhausted (the terminal
// drain runs on a bounded context, and a timeout is itself a common cause), so
// the FACT write uses a FRESH background context: a loss that cannot be recorded
// is a loss that stays invisible, which is exactly what art.4.5 forbids.
func (s *CaseRunner) recordSettlementFailure(ctx context.Context, caseRun execution.CaseRun, session execution.CaseRunSUTContext, count int64, cause error) {
	slog.ErrorContext(ctx, "execution evidence settlement failed; the un-settled recording is counted as dropped",
		"case_run_id", caseRun.ID, "session_id", session.SessionID,
		"artifact_cursor", session.ArtifactCursor, "dropped", count, "error", cause)
	factCtx, cancel := context.WithTimeout(context.Background(), evidenceDropFactTimeout)
	defer cancel()
	s.recordEvidenceDrops(factCtx, caseRun, count, "evidence_settlement_failed", execution.EvidenceKind(""), nil)
}

// --- http tool -------------------------------------------------------------

type httpToolArgs struct {
	SUTID     string            `json:"sut_id,omitempty"`
	APIID     string            `json:"api_id,omitempty"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMS *int              `json:"timeout_ms,omitempty"`
}

// HTTP executes http.request against ANY host (v0.2.4-C T12/P8: the SUT-host
// gate is lifted). A relative path still convenience-prepends the SUT base URL;
// an absolute http(s) URL is used verbatim without a belonging check. The FULL
// response body spills to the content ledger with an explicit truncated flag and
// a content_ref addressing the full content; large headers are size-bounded
// explicitly (Ruling T5-C), never silently scrubbed and never a 413 whole-call
// failure. Headers and bodies are recorded plaintext (GD-01/Q05=B).
func (s *CaseRunner) HTTP(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.httpClient == nil {
		return nil, errors.New("case runner has no HTTP client")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	var in httpToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid http.request arguments")
	}
	sutID, err := s.resolveSUTID(ctx, scope, optionalToolID(in.SUTID))
	if err != nil {
		return nil, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	path := strings.TrimSpace(in.Path)
	if in.APIID != "" {
		apiID, parseErr := parseToolID("api_id", in.APIID)
		if parseErr != nil {
			return nil, parseErr
		}
		if s.apis == nil {
			return nil, platform.Invalid("api_id", "api_id resolution is not configured")
		}
		api, apiErr := s.apis.API(ctx, scope.VersionID, sutID, apiID)
		if apiErr != nil {
			return nil, mapExecutionError(apiErr, "api")
		}
		if method == "" {
			method = strings.ToUpper(api.Method)
		}
		if path == "" {
			path = api.Path
		}
	}
	if path == "" {
		return nil, platform.Invalid("path", "path or api_id is required")
	}
	targetURL, err := resolveSUTURL(binding, path)
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = "GET"
	}
	headers := make(map[string]string, len(in.Headers))
	for name, value := range in.Headers {
		headers[name] = value
	}
	request := HTTPRequestData{Method: method, URL: targetURL, Headers: headers, Body: []byte(in.Body)}
	if in.TimeoutMS != nil && *in.TimeoutMS > 0 {
		request.TimeoutMS = *in.TimeoutMS
	}
	response, err := s.httpClient.Do(ctx, request)
	if err != nil {
		return nil, mapExecutionError(err, "http")
	}
	// Ruling T5-C: header SIZE bounding (explicit, plaintext up to the bound) —
	// not a security scrub. Pathological large headers must not 413 the call.
	reqHeaders, reqHeadersTruncated := boundRequestHeaderValues(headers)
	respHeaders, respHeadersTruncated := boundResponseHeaderValues(response.Headers)
	headersTruncated := reqHeadersTruncated || respHeadersTruncated

	fullBody := response.Body
	bodySHA := sha256HexBytes(fullBody)
	bodyPreview := truncateBytes(fullBody, httpEvidenceBodyPreview)
	// GD-01 / Q05=B (R-C3): request/response headers enter evidence and the tool
	// result plaintext; credentials may appear in evidence by design. The evidence
	// envelope keeps a bounded body preview plus the full-body sha; the FULL body
	// is spilled separately below so the envelope never 413s on a large response.
	evidence := map[string]any{
		"request": map[string]any{
			"method":            method,
			"url":               targetURL,
			"headers":           reqHeaders,
			"headers_truncated": reqHeadersTruncated,
			"body":              truncateString(in.Body, 4000),
			"body_truncated":    utf8.RuneCountInString(in.Body) > 4000,
		},
		"response": map[string]any{
			"status":            response.Status,
			"headers":           respHeaders,
			"headers_truncated": respHeadersTruncated,
			"body":              string(bodyPreview),
			"body_truncated":    len(fullBody) > len(bodyPreview),
			"body_bytes":        len(fullBody),
			"body_sha256":       bodySHA,
		},
	}
	encoded, _ := json.Marshal(evidence)
	versionRef := sutID
	stored, err := s.AddEvidence(ctx, tools.Scope{
		VersionID:     scope.VersionID,
		WaveID:        caseRun.WaveID,
		CaseRunID:     caseRun.ID,
		IntentID:      caseRun.IntentID,
		CaseVersionID: caseRun.CaseVersionID,
		DispatchEpoch: caseRun.DispatchEpoch,
		DispatchToken: caseRun.DispatchToken,
	}, EvidenceInput{Kind: execution.EvidenceHTTP, Data: encoded, Filename: "http-request.json", SUTID: &versionRef, Auto: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"status":      response.Status,
		"duration_ms": response.DurationMS,
		"headers":     respHeaders,
		"body":        truncateString(string(fullBody), httpInlineBodyBound),
	}
	if headersTruncated {
		out["headers_truncated"] = true
	}
	if stored.ID > 0 {
		out["evidence_id"] = platformid.Format(stored.ID)
	} else {
		// Explicit cap-drop signal (execution.evidence_max_per_case_run); the
		// unified shape is the dropped COUNT, same as every other tool path.
		out["evidence_skipped"] = 1
		out["evidence_skipped_reason"] = "evidence_cap_reached"
	}
	// P8 spill: when the body exceeds the inline bound, write the FULL raw body to
	// the content ledger (tied to the evidence row) and point content_ref at it.
	// tool_result.read(content_ref) then reads the complete body back; the
	// truncated flag is explicit (no silent 64KB loss).
	if len(fullBody) > httpInlineBodyBound {
		out["head"] = truncateString(string(fullBody), 200)
		out["truncated"] = true
		out["body_bytes"] = len(fullBody)
		if stored.ID > 0 && s.content != nil {
			ref, spillErr := s.content.PutEvidence(ctx, stored.ID, execution.EvidenceHTTP, "http-body.bin", fullBody)
			if spillErr != nil {
				return nil, mapExecutionError(spillErr, "content")
			}
			out["content_ref"] = ref.SHA256
		}
	}
	return out, nil
}

// --- db tools --------------------------------------------------------------

type dbQueryToolArgs struct {
	SUTID              string `json:"sut_id,omitempty"`
	DatabaseID         string `json:"database_id,omitempty"`
	SQL                string `json:"sql"`
	MaxRows            *int   `json:"max_rows,omitempty"`
	StatementTimeoutMS *int   `json:"statement_timeout_ms,omitempty"`
}

// DBQuery executes one general SQL statement (read or write) on the SUT-bound
// PostgreSQL database and stores the SQL/result as db evidence. v0.2.4-C T12
// (P8): the read-only gate and the hard 200-row cap are lifted; rows are
// unbounded unless max_rows is passed, and the statement timeout is configurable.
// A result larger than the evidence byte budget spills the FULL result to the
// content ledger with a bounded envelope (explicit truncation, never a silent
// 64KB cut and never a 413).
func (s *CaseRunner) DBQuery(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.database == nil || s.credentials == nil {
		return nil, errors.New("case runner has no database adapter")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	var in dbQueryToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid db.query arguments")
	}
	sutID, err := s.resolveSUTID(ctx, scope, optionalToolID(in.SUTID))
	if err != nil {
		return nil, err
	}
	var databaseID *int64
	if in.DatabaseID != "" {
		id, parseErr := parseToolID("database_id", in.DatabaseID)
		if parseErr != nil {
			return nil, parseErr
		}
		databaseID = &id
	}
	if strings.TrimSpace(in.SQL) == "" {
		return nil, platform.Invalid("sql", "sql is required")
	}
	// P8: no read-only gate. MaxRows <= 0 (omitted) means unlimited.
	opts := DBQueryOptions{}
	if in.MaxRows != nil && *in.MaxRows > 0 {
		opts.MaxRows = *in.MaxRows
	}
	if in.StatementTimeoutMS != nil && *in.StatementTimeoutMS > 0 {
		opts.StatementTimeoutMS = *in.StatementTimeoutMS
	}
	credential, err := s.credentials.Database(ctx, scope.VersionID, sutID, databaseID)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	result, err := s.database.Query(ctx, credential.DSN, credential.Username, credential.Password, in.SQL, opts)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	fullResult := map[string]any{
		"sql":           in.SQL,
		"columns":       result.Columns,
		"rows":          result.Rows,
		"row_count":     result.RowCount,
		"truncated":     result.Truncated,
		"rows_affected": result.RowsAffected,
		"command_tag":   result.CommandTag,
	}
	encoded, _ := json.Marshal(fullResult)
	// P8 evidence spill: store the FULL result when it fits the evidence budget;
	// otherwise spill the full result to the ledger and keep a bounded envelope
	// with an explicit result_truncated flag (the old silent 64KB cut is removed).
	budget := s.intSetting("execution.evidence_max_bytes", defaultEvidenceMaxBytes)
	evidenceData := encoded
	spillFull := int64(len(encoded)) > budget
	if spillFull {
		evidenceData = boundedDBResultEnvelope(fullResult, result.Rows, encoded, budget)
	}
	versionRef := sutID
	stored, err := s.AddEvidence(ctx, tools.Scope{
		VersionID:     scope.VersionID,
		WaveID:        caseRun.WaveID,
		CaseRunID:     caseRun.ID,
		IntentID:      caseRun.IntentID,
		CaseVersionID: caseRun.CaseVersionID,
		DispatchEpoch: caseRun.DispatchEpoch,
		DispatchToken: caseRun.DispatchToken,
	}, EvidenceInput{Kind: execution.EvidenceDB, Data: evidenceData, Filename: "db-query.json", SUTID: &versionRef, Auto: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"columns":   result.Columns,
		"rows":      result.Rows,
		"row_count": result.RowCount,
		"truncated": result.Truncated,
	}
	if result.CommandTag != "" {
		out["command_tag"] = result.CommandTag
	}
	if result.RowsAffected != 0 {
		out["rows_affected"] = result.RowsAffected
	}
	if stored.ID > 0 {
		out["evidence_id"] = platformid.Format(stored.ID)
	} else {
		// Explicit cap-drop signal (execution.evidence_max_per_case_run); the
		// unified shape is the dropped COUNT, same as every other tool path.
		out["evidence_skipped"] = 1
		out["evidence_skipped_reason"] = "evidence_cap_reached"
	}
	if spillFull && stored.ID > 0 && s.content != nil {
		ref, spillErr := s.content.PutEvidence(ctx, stored.ID, execution.EvidenceDB, "db-result.json", encoded)
		if spillErr != nil {
			return nil, mapExecutionError(spillErr, "content")
		}
		out["content_ref"] = ref.SHA256
		out["result_truncated"] = true
	}
	return out, nil
}

type schemaReadToolArgs struct {
	SUTID      string `json:"sut_id,omitempty"`
	DatabaseID string `json:"database_id,omitempty"`
	Schema     string `json:"schema,omitempty"`
}

// SchemaRead returns a bounded tables/columns summary of the target database.
func (s *CaseRunner) SchemaRead(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.database == nil || s.credentials == nil {
		return nil, errors.New("case runner has no database adapter")
	}
	var in schemaReadToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid database.schema_read arguments")
	}
	sutID, err := s.resolveSUTID(ctx, scope, optionalToolID(in.SUTID))
	if err != nil {
		return nil, err
	}
	var databaseID *int64
	if in.DatabaseID != "" {
		id, parseErr := parseToolID("database_id", in.DatabaseID)
		if parseErr != nil {
			return nil, parseErr
		}
		databaseID = &id
	}
	credential, err := s.credentials.Database(ctx, scope.VersionID, sutID, databaseID)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	tables, err := s.database.Schema(ctx, credential.DSN, credential.Username, credential.Password, in.Schema)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	return map[string]any{
		"sut_id":      platformid.Format(sutID),
		"tables":      tables,
		"table_count": len(tables),
	}, nil
}

// --- read/analysis tools ---------------------------------------------------

// ResultRead returns the wave-scoped case_run/step/assertion result summary.
func (s *CaseRunner) ResultRead(ctx context.Context, scope tools.Scope, in tools.ResultReadInput) (any, error) {
	if scope.WaveID <= 0 {
		return nil, platform.Invalid("scope", "result_read requires the injected wave scope")
	}
	caseRuns, err := s.caseRuns.ListAllByWave(ctx, scope.WaveID)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	sort.SliceStable(caseRuns, func(i, j int) bool { return caseRuns[i].ID < caseRuns[j].ID })
	results := make([]map[string]any, 0, len(caseRuns))
	for _, caseRun := range caseRuns {
		if in.CaseRunID != nil && *in.CaseRunID != caseRun.ID {
			continue
		}
		steps, _ := s.results.ListSteps(ctx, caseRun.ID)
		assertions, _ := s.results.ListAssertions(ctx, caseRun.ID)
		evidenceCount, _ := s.evidence.CountByCaseRun(ctx, caseRun.ID)
		stepEntries := make([]map[string]any, 0, len(steps))
		for _, step := range steps {
			stepEntries = append(stepEntries, map[string]any{
				"seq": step.Seq, "status": string(step.Status),
				"observed": step.Observed, "error": step.Error,
			})
		}
		assertionEntries := make([]map[string]any, 0, len(assertions))
		for _, assertion := range assertions {
			assertionEntries = append(assertionEntries, map[string]any{
				"step_seq": assertion.StepSeq, "assertion_seq": assertion.AssertionSeq,
				"kind": assertion.Kind, "verdict": string(assertion.Verdict),
				"expected": assertion.Expected, "actual": assertion.Actual, "reason": assertion.Reason,
			})
		}
		result := map[string]any{
			"case_run_id":     platformid.Format(caseRun.ID),
			"case_version_id": platformid.Format(caseRun.CaseVersionID),
			"status":          string(caseRun.Status),
			"failure_class":   string(caseRun.FailureClass),
			"verdict_reason":  caseRun.Summary.VerdictReason,
			"steps":           stepEntries,
			"assertions":      assertionEntries,
			"evidence_count":  evidenceCount,
		}
		if attribution := s.resultFailureAttribution(ctx, scope.VersionID, caseRun, steps, assertions); attribution != nil {
			result["attribution"] = attribution
		}
		results = append(results, result)
	}
	return map[string]any{"wave_id": platformid.Format(scope.WaveID), "case_runs": results}, nil
}

// CoverageRead returns requirement/case execution coverage for the version.
func (s *CaseRunner) CoverageRead(ctx context.Context, scope tools.Scope) (any, error) {
	if s.coverage == nil || s.cases == nil {
		return nil, errors.New("case runner has no coverage reader")
	}
	requirements, err := s.coverage.ListRequirements(ctx, scope.VersionID)
	if err != nil {
		return nil, mapExecutionError(err, "requirement")
	}
	cases, err := s.cases.ListExecutable(ctx, scope.VersionID)
	if err != nil {
		return nil, mapCaseReadError(err)
	}
	caseIDsByRequirement := map[int64][]int64{}
	allCaseVersionIDs := make([]int64, 0, len(cases))
	for _, snapshot := range cases {
		caseIDsByRequirement[snapshot.RequirementID] = append(caseIDsByRequirement[snapshot.RequirementID], snapshot.CaseVersionID)
		allCaseVersionIDs = append(allCaseVersionIDs, snapshot.CaseVersionID)
	}
	latest, err := s.caseRuns.LatestByCaseVersions(ctx, scope.VersionID, allCaseVersionIDs)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	entries := make([]map[string]any, 0, len(requirements))
	for _, requirement := range requirements {
		caseVersionIDs := caseIDsByRequirement[requirement.RequirementID]
		entry := map[string]any{
			"requirement_id":         platformid.Format(requirement.RequirementID),
			"requirement_version_id": platformid.Format(requirement.RequirementVersionID),
			"title":                  requirement.Title,
			"status":                 requirement.Status,
			"case_version_count":     len(caseVersionIDs),
		}
		covered := false
		executed := false
		failed := false
		for _, caseVersionID := range caseVersionIDs {
			caseRun, ok := latest[caseVersionID]
			if !ok {
				continue
			}
			executed = true
			if caseRun.Status == execution.CaseRunPassed {
				covered = true
			}
			if caseRun.Status == execution.CaseRunFailed || caseRun.Status == execution.CaseRunBlocked ||
				caseRun.Status == execution.CaseRunInconclusive {
				failed = true
			}
		}
		switch {
		case covered:
			entry["coverage"] = "covered"
		case executed && failed:
			entry["coverage"] = "gap"
			entry["gap_reason"] = "execution_failed_coverage"
		case len(caseVersionIDs) > 0:
			entry["coverage"] = "gap"
			entry["gap_reason"] = "execution_not_run"
		default:
			entry["coverage"] = "no_cases"
		}
		entries = append(entries, entry)
	}
	return map[string]any{"version_id": platformid.Format(scope.VersionID), "requirements": entries}, nil
}

// executorCapabilityEscalationError returns a state_conflict when a run carrying
// the structured executor limitation marker is classified as an application (SUT)
// defect. It is shared by the production CaseRunner.FailureSubmit and
// AnalysisService.SubmitFailure so both analyst entry points enforce the X-02
// no-escalation rule.
func executorCapabilityEscalationError(steps []execution.StepResult) error {
	step, code, limited := execution.ExecutorLimitationStep(steps)
	if !limited {
		return nil
	}
	reason := "executor capability limitation"
	if code != "" {
		reason = "executor capability limitation " + code
	}
	return platform.StateConflict(fmt.Sprintf("%s on step %d cannot be classified as an application defect", reason, step.Seq))
}

// FailureSubmit validates and persists one failure analysis candidate. Only
// failed(application) is a P4 issue candidate; the analyst may refine but may
// upgrade a non-application class into application only when the stored step
// observation is a requirement deviation (QA Q1).
func (s *CaseRunner) FailureSubmit(ctx context.Context, scope tools.Scope, in tools.FailureSubmitInput) (any, error) {
	if scope.WaveID <= 0 {
		return nil, platform.Invalid("scope", "failure_submit requires the injected wave scope")
	}
	caseRun, err := s.caseRuns.Get(ctx, in.CaseRunID)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	if caseRun.WaveID != scope.WaveID {
		return nil, platform.NotFound("case_run")
	}
	if caseRun.Status != execution.CaseRunFailed && caseRun.Status != execution.CaseRunBlocked &&
		caseRun.Status != execution.CaseRunInconclusive {
		return nil, platform.StateConflict("failure analysis requires a finished failed/blocked/inconclusive case_run")
	}
	class := execution.FailureClass(in.FailureClass)
	if !class.Valid() || class == "" {
		return nil, platform.Invalid("failure_class", "unsupported failure class")
	}
	var steps []execution.StepResult
	stepsLoaded := false
	loadSteps := func() ([]execution.StepResult, error) {
		if stepsLoaded {
			return steps, nil
		}
		loaded, err := s.results.ListSteps(ctx, caseRun.ID)
		if err != nil {
			return nil, mapExecutionError(err, "step_result")
		}
		steps, stepsLoaded = loaded, true
		return steps, nil
	}
	if class == execution.FailureClassApplication {
		// v0.2.3 X-02: a run limited by the executor's own capability is an
		// execution-environment condition, never a SUT application defect. The
		// analyst cannot escalate it, even with another unmarked deviation or a
		// failed assertion.
		steps, err = loadSteps()
		if err != nil {
			return nil, err
		}
		if err := executorCapabilityEscalationError(steps); err != nil {
			return nil, err
		}
		if caseRun.FailureClass != execution.FailureClassApplication {
			// QA Q1: a stored observed requirement deviation is evidence the
			// deterministic fallback may have missed; allow application only for
			// that evidence.
			hasDeviation := false
			for _, step := range steps {
				if execution.StepShowsApplicationDeviation(step) {
					hasDeviation = true
					break
				}
			}
			if !hasDeviation {
				return nil, platform.Invalid("failure_class", "a non-application failure cannot be upgraded to application without observed requirement deviation evidence")
			}
		}
	}
	if class == execution.FailureClassAutomation && caseRun.FailureClass == execution.FailureClassApplication {
		// QA Q1: a real requirement deviation recorded on a failed step is
		// product evidence; the analyst must not be able to dismiss it as a
		// script/automation problem and suppress the issue candidate.
		steps, err = loadSteps()
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			if execution.StepShowsApplicationDeviation(step) {
				return nil, platform.StateConflict("an observed requirement deviation cannot be reclassified as automation")
			}
		}
	}
	updated := caseRun
	updated.FailureClass = class
	summary := execution.SanitizeFailureSummary(in.Summary)
	if summary != "" {
		updated.Summary.LastError = truncateRunes(summary, 2000)
	}
	if _, err := s.caseRuns.Update(ctx, updated); err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	dedupe := in.DedupeHint
	steps, _ = loadSteps()
	assertions, _ := s.results.ListAssertions(ctx, caseRun.ID)
	if dedupe == "" {
		// Align with the deterministic §8.1 fallback so P4 sees one stable
		// dedupe hint for both paths.
		dedupe = failureDedupeHint(scope.VersionID, caseRun, class, steps, assertions)
	}
	// Task 112: never echo the analyst's suggested category/severity. They are a
	// second source that can disagree with the P4 issue record (and the summary);
	// emit the canonical derivation instead.
	category, severity := s.caseIssueAttribution(ctx, scope.VersionID, caseRun, steps, assertions)
	eventScope := map[string]any{
		"wave_id":     platformid.Format(caseRun.WaveID),
		"case_run_id": platformid.Format(caseRun.ID),
	}
	publishEvent(context.Background(), s.events, EventFailureCandidate, s.versionForCaseRun(caseRun), eventScope, map[string]any{
		"case_run_id":     platformid.Format(caseRun.ID),
		"case_version_id": platformid.Format(caseRun.CaseVersionID),
		"failure_class":   string(class),
		"category":        category,
		"severity":        severity,
		"summary":         truncateRunes(summary, 2000),
		"dedupe_hint":     dedupe,
		"issue_candidate": class.IsIssueCandidate(),
	}, nil)
	if s.teamRoom != nil && class.IsIssueCandidate() {
		_ = s.teamRoom.Post(context.Background(), TeamPost{
			VersionID: s.versionForCaseRun(caseRun),
			Team:      "execution",
			Kind:      "finding",
			Content:   truncateRunes(summary, 2000),
			Refs:      []TeamPostRef{{Kind: "case_run", ID: platformid.Format(caseRun.ID)}},
		})
	}
	return map[string]any{
		"case_run_id":   platformid.Format(caseRun.ID),
		"failure_class": string(class),
		"category":      category,
		"severity":      severity,
		"dedupe_hint":   dedupe,
	}, nil
}

// CoverageSubmit validates one coverage gap candidate and ensures design.gap.
func (s *CaseRunner) CoverageSubmit(ctx context.Context, scope tools.Scope, in tools.CoverageSubmitInput) (any, error) {
	if scope.VersionID <= 0 {
		return nil, platform.Invalid("scope", "coverage_submit requires the injected version scope")
	}
	if s.coverage == nil || s.gaps == nil {
		return nil, errors.New("case runner has no coverage analyzer")
	}
	requirements, err := s.coverage.ListRequirements(ctx, scope.VersionID)
	if err != nil {
		return nil, mapExecutionError(err, "requirement")
	}
	found := false
	for _, requirement := range requirements {
		if requirement.RequirementID == in.RequirementID {
			found = true
			break
		}
	}
	if !found {
		return nil, platform.NotFound("requirement")
	}
	switch in.Reason {
	case "execution_not_run", "execution_failed_coverage":
	default:
		return nil, platform.Invalid("reason", "unsupported coverage gap reason")
	}
	for _, caseRunID := range in.EvidenceCaseRunIDs {
		caseRun, getErr := s.caseRuns.Get(ctx, caseRunID)
		if getErr != nil {
			return nil, mapExecutionError(getErr, "case_run")
		}
		if scope.WaveID > 0 && caseRun.WaveID != scope.WaveID {
			return nil, platform.Invalid("evidence_case_run_ids", "evidence case_run is outside the wave")
		}
	}
	if err := s.gaps.EnsureGap(ctx, scope.VersionID, in.RequirementID, 0, in.Reason); err != nil {
		return nil, mapExecutionError(err, "design_gap")
	}
	publishEvent(context.Background(), s.events, EventCoverageGap, scope.VersionID, map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
	}, map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
	}, nil)
	if s.teamRoom != nil {
		_ = s.teamRoom.Post(context.Background(), TeamPost{
			VersionID: scope.VersionID,
			Team:      "execution",
			Kind:      "blocker",
			Content:   "执行覆盖缺口: " + in.Reason,
			Refs:      []TeamPostRef{{Kind: "requirement", ID: platformid.Format(in.RequirementID)}},
		})
	}
	return map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
		"ensured":        true,
	}, nil
}

// HumanRequest delegates the login-takeover pause to the Task 9 human service.
func (s *CaseRunner) HumanRequest(ctx context.Context, scope tools.Scope, in tools.HumanRequestInput) (any, error) {
	if s.human == nil {
		return nil, errors.New("case runner has no human request service")
	}
	humanRequestID, err := s.human.CreateFromTool(ctx, scope, in)
	if err != nil {
		return nil, mapExecutionError(err, "human_request")
	}
	return map[string]any{
		"human_request_id": platformid.Format(humanRequestID),
		"status":           string(execution.HumanPending),
		"note":             "case_run is paused until the request is resolved; stop this run",
	}, nil
}

// CaseFinish is the execution.case_finish tool candidate wrapper.
func (s *CaseRunner) CaseFinish(ctx context.Context, scope tools.Scope, in tools.CaseFinishInput) (any, error) {
	finished, err := s.Finish(ctx, scope, execution.FinishCandidate{
		Verdict:      execution.CaseRunStatus(in.Verdict),
		FailureClass: execution.FailureClass(in.FailureClass),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"case_run_id":    platformid.Format(finished.ID),
		"status":         string(finished.Status),
		"failure_class":  string(finished.FailureClass),
		"verdict_reason": finished.Summary.VerdictReason,
	}, nil
}

// --- helpers ---------------------------------------------------------------

func optionalToolID(value string) *int64 {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return nil
	}
	return &parsed
}

func parseToolID(field, value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

func defaultBaseURL(binding execution.SUTBinding) string {
	if len(binding.PageURLs) > 0 {
		if parsed, err := url.Parse(binding.PageURLs[0]); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host
		}
	}
	return ""
}

func urlHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("invalid scheme")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

// resolveSUTURL resolves the http.request target. v0.2.4-C T12 (P8): an absolute
// http(s) URL is used verbatim against ANY host — the old SUT-host belonging gate
// is lifted. A relative path still convenience-prepends the SUT base URL (and
// requires one).
func resolveSUTURL(binding execution.SUTBinding, path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		if _, err := urlHost(path); err != nil {
			return "", platform.Invalid("path", "must be an absolute http(s) URL")
		}
		return path, nil
	}
	base := binding.BaseURL
	if base == "" {
		base = defaultBaseURL(binding)
	}
	if base == "" {
		return "", platform.StateConflict("the SUT has no base URL")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", platform.StateConflict("the SUT base URL is invalid")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(base, "/") + path, nil
}

// failureDedupeHint implements the deterministic P3 §8.1 candidate key:
// sha256(version_id|case_version_id|step_seq|assertion_seq|failure_class)[:32].
// The first failed assertion (or failed step when no assertion failed) is used.
func failureDedupeHint(versionID int64, caseRun execution.CaseRun, class execution.FailureClass, steps []execution.StepResult, assertions []execution.AssertionResult) string {
	stepSeq, assertionSeq := 0, 0
	for _, assertion := range assertions {
		if assertion.Verdict == execution.AssertionFailed {
			stepSeq, assertionSeq = assertion.StepSeq, assertion.AssertionSeq
			break
		}
	}
	if stepSeq == 0 {
		for _, step := range steps {
			if step.Status == execution.StepFailed {
				stepSeq = step.Seq
				break
			}
		}
	}
	payload := strconv.FormatInt(versionID, 10) + "|" +
		strconv.FormatInt(caseRun.CaseVersionID, 10) + "|" +
		strconv.Itoa(stepSeq) + "|" + strconv.Itoa(assertionSeq) + "|" + string(class)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:32]
}

func truncateBytes(value []byte, limit int) []byte {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func truncateString(value string, limit int) string {
	return truncateRunes(value, limit)
}

// --- P8 lift helpers (v0.2.4-C T12) -----------------------------------------

const (
	// httpInlineBodyBound is the inline body preview kept in the http.request tool
	// result; a larger body spills the FULL content to the ledger (content_ref).
	httpInlineBodyBound = 4000
	// httpEvidenceBodyPreview is the bounded body preview kept in the http
	// evidence envelope; the FULL body is spilled separately so the envelope never
	// 413s on a large response.
	httpEvidenceBodyPreview = 64 << 10
	// headerValueCharBound is the per-value header SIZE bound (Ruling T5-C). It is
	// a size bound, NOT a security scrub: values stay plaintext up to the bound and
	// any cut is flagged explicitly (headers_truncated). It restores the sane
	// header sizing the pre-T5 redactHeaders pass incidentally provided, without
	// re-introducing redaction.
	headerValueCharBound = 500
)

// sha256HexBytes returns the lowercase hex SHA-256 of data — the content-ledger address
// (content_ref) tool_result.read reads back.
func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// boundHeaderValue cuts one header value to the size bound, reporting whether it
// was cut. Plaintext up to the bound (Ruling T5-C: size, not scrub).
func boundHeaderValue(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) <= headerValueCharBound {
		return value, false
	}
	return string(runes[:headerValueCharBound]), true
}

// boundRequestHeaderValues size-bounds the request headers (map[string]string),
// preserving shape, and reports whether any value was cut.
func boundRequestHeaderValues(headers map[string]string) (map[string]string, bool) {
	out := make(map[string]string, len(headers))
	truncated := false
	for name, value := range headers {
		bounded, cut := boundHeaderValue(value)
		out[name] = bounded
		truncated = truncated || cut
	}
	return out, truncated
}

// boundResponseHeaderValues size-bounds the response headers
// (map[string][]string), preserving shape, and reports whether any value was cut.
func boundResponseHeaderValues(headers map[string][]string) (map[string][]string, bool) {
	out := make(map[string][]string, len(headers))
	truncated := false
	for name, values := range headers {
		bounded := make([]string, len(values))
		for i, value := range values {
			b, cut := boundHeaderValue(value)
			bounded[i] = b
			truncated = truncated || cut
		}
		out[name] = bounded
	}
	return out, truncated
}

// boundedDBResultEnvelope builds a db evidence envelope that fits the evidence
// byte budget: it keeps the statement metadata plus a shrinking rows preview and
// flags result_truncated explicitly, carrying the full-result sha so the spilled
// full result stays traceable. The FULL result is spilled separately by the caller.
func boundedDBResultEnvelope(fullResult map[string]any, rows [][]string, fullEncoded []byte, budget int64) []byte {
	preview := rows
	for {
		envelope := make(map[string]any, len(fullResult)+2)
		for key, value := range fullResult {
			envelope[key] = value
		}
		envelope["rows"] = preview
		envelope["result_truncated"] = true
		envelope["result_sha256"] = sha256HexBytes(fullEncoded)
		data, _ := json.Marshal(envelope)
		if int64(len(data)) <= budget || len(preview) == 0 {
			return data
		}
		preview = preview[:len(preview)/2]
	}
}
