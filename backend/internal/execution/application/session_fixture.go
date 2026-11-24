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
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// P18 session fixture library (v0.2.4-F, SC-F5; design v1.3.1 §6.4).
//
// The execution team saves a login storage-state through the official CLI
// (browser.cli state-save writes a file into the case_run workspace), uploads
// it to this SERVER-STORED fixture library, and a later case_run of the same
// version reuses it: the download tool fetches the current fixture, writes it
// into the new session workspace and injects it via CLI state-load. This is the
// cross-case reuse layer ABOVE the per-run executor session (whose key stays
// run:<id>:sut:<sut>:role:<role>): the reuse key here is
// (version, sut, environment, role).
//
// Storage model: append-only fixture-version facts (the T2 SessionFixture port,
// Append/List only) + a DERIVED current fixture (the newest fact of one reuse
// key). There is no mutable current pointer and no Update/Delete on the fact
// store; re-saving appends a new fact and the next download derives the latest.
// The state bytes are content-addressed in the content ledger (sha256); the
// fact stores only the reference. case_run_sut_context records the fixture
// reference (the reuse link) on both save and inject.
//
// Failure path (Review Focus 5): an expired/invalid fixture is NEVER silently
// used. A missing fixture is not_found; a missing/corrupt content blob is a
// typed fixture_content_unavailable; a failed CLI state-load is a typed
// fixture_load_failed. Each tells the model to re-login (or re-fetch) and
// re-save; nothing is injected on failure and no reuse link is recorded.
//
// GD-01 / capability-first: the library never narrows the agent's operation
// surface — the agent still logs in autonomously through browser.cli/http/exec
// (GD-21); the fixture library only makes a proven login state reusable. The
// login_takeover human judgment point (GD-02) stays optional: a 2FA challenge
// pauses the run for a human, and the resumed run saves the state here.

// EventSessionSaved announces one saved session fixture on the version channel.
// The payload carries the decimal case_run/fixture/sut/environment ids, the
// role and the content-addressed storage_state_ref. The login_session discovery
// fact chain (case_author producer) consumes this event/fact downstream.
const EventSessionSaved = "session.saved"

// Default workspace paths of the fixture chain. The upload default matches the
// CLI convention (state-save state.json); the download default is a DIFFERENT
// file so injecting an older fixture never clobbers a freshly saved state file.
const (
	DefaultFixtureUploadPath   = "state.json"
	DefaultFixtureDownloadPath = "fixture-state.json"
)

// maxFixtureStateBytes bounds one storage-state blob (the executor's JSON file
// transfer bound). A larger file fails explicitly, never silently truncated.
const maxFixtureStateBytes = 4 << 20

// FixtureContentStore is the content-ledger surface of the fixture library:
// PutFixture stores the state bytes content-addressed under the fixture fact's
// owner reference; Open reads verified bytes back by sha256 (the storage
// service re-verifies hash/size). *adapters.ContentStoreAdapter satisfies it.
type FixtureContentStore interface {
	PutFixture(ctx context.Context, fixtureID int64, filename string, data []byte) (ContentRef, error)
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// FixtureSessionPort is the per-(SUT, account_role) executor-session surface the
// fixture library needs. *CaseRunner implements it over the SAME fenced session
// the execution tools use (dispatch fence, lazy session creation, recording
// drain), so a fixture save/load is recorded exactly like any other file/command
// operation. Keeping it a narrow port lets the service be unit-tested without a
// live executor.
type FixtureSessionPort interface {
	// ReadSessionFile downloads one workspace file of the run's session.
	ReadSessionFile(ctx context.Context, scope tools.Scope, sutID int64, role, path string) ([]byte, error)
	// WriteSessionFile uploads bytes into the run's session workspace.
	WriteSessionFile(ctx context.Context, scope tools.Scope, sutID int64, role, path string, data []byte) error
	// LoadBrowserState injects a workspace storage-state file via the official
	// CLI state-load. A non-zero exit is a typed fixture_load_failed error.
	LoadBrowserState(ctx context.Context, scope tools.Scope, sutID int64, role, path string) error
	// LinkSessionFixture records the fixture reference on the run's
	// case_run_sut_context row (the cross-case reuse link).
	LinkSessionFixture(ctx context.Context, scope tools.Scope, sutID int64, role string, fixtureID int64) error
}

// SessionFixtureVersionReader is the admin visibility listing (SC-F5): one
// version's fixture facts newest first, metadata only (never the state bytes).
// It is paginated (beforeID/limit, limit+1 probe) because fixture facts are
// append-only and unbounded — a long-lived version with repeated re-logins
// exceeds any fixed cap, so the listing must page honestly (hasMore/nextCursor)
// rather than silently truncate (constitution art.7.6).
// *executionpg.SessionFixtureRepository satisfies it.
type SessionFixtureVersionReader interface {
	ListSessionFixturesByVersion(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.SessionFixture, bool, error)
}

// SessionFixtureDeps wires the P18 fixture library. Fixtures, Catalog, Content
// and Session are required; Events/Clock are optional (a nil publisher skips
// the event, a nil clock falls back to the DB created_at default).
type SessionFixtureDeps struct {
	Fixtures execution.SessionFixtureRepository
	Catalog  SUTCatalogReader
	Content  FixtureContentStore
	Session  FixtureSessionPort
	Events   EventPublisher
	Clock    Clock
}

// SessionFixtureService is the server-side session fixture library: it derives
// the current fixture of a reuse key, gates the reuse key against the version's
// EFFECTIVE SUT catalog (a proposed/rejected SUT never enters the library), and
// records the append-only facts + reuse links.
type SessionFixtureService struct {
	fixtures execution.SessionFixtureRepository
	catalog  SUTCatalogReader
	content  FixtureContentStore
	session  FixtureSessionPort
	events   EventPublisher
	clock    Clock
}

// NewSessionFixtureService wires the fixture library service.
func NewSessionFixtureService(deps SessionFixtureDeps) *SessionFixtureService {
	return &SessionFixtureService{
		fixtures: deps.Fixtures,
		catalog:  deps.Catalog,
		content:  deps.Content,
		session:  deps.Session,
		events:   deps.Events,
		clock:    deps.Clock,
	}
}

// UploadFixture saves the login state the agent wrote via CLI state-save into
// the server fixture library: it reads the workspace file, validates it is a
// storage-state JSON object, appends the append-only fixture fact under the
// (version, sut, environment, role) reuse key, stores the bytes content-
// addressed, records the reuse link on case_run_sut_context and publishes
// session.saved. An invalid file / out-of-scope SUT / escaping path is rejected
// before any write; an infra failure propagates (observable, retryable).
func (s *SessionFixtureService) UploadFixture(ctx context.Context, scope tools.Scope, in tools.FixtureUploadInput) (any, error) {
	if s.fixtures == nil || s.catalog == nil || s.content == nil || s.session == nil {
		return nil, platform.Unavailable("session fixture service is not configured")
	}
	if scope.CaseRunID <= 0 {
		return nil, platform.Invalid("case_run_id", "fixture upload requires an execution case_run scope")
	}
	environmentID, err := s.environmentOf(ctx, scope.VersionID, in.SUTID)
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(in.AccountRole)
	path, err := fixtureWorkspacePath(in.Path, DefaultFixtureUploadPath)
	if err != nil {
		return nil, err
	}
	state, err := s.session.ReadSessionFile(ctx, scope, in.SUTID, role, path)
	if err != nil {
		return nil, err
	}
	if err := validateFixtureState(state); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(state)
	fact := execution.SessionFixture{
		VersionID:       scope.VersionID,
		SUTID:           in.SUTID,
		EnvironmentID:   environmentID,
		Role:            normalizeFixtureRole(role),
		StorageStateRef: hex.EncodeToString(sum[:]),
		CreatedBy:       scope.AgentRunID,
	}
	if err := fact.Validate(); err != nil {
		return nil, err
	}
	if s.clock != nil {
		fact.CreatedAt = s.clock.Now()
	}
	written, err := s.fixtures.AppendSessionFixture(ctx, fact)
	if err != nil {
		return nil, err
	}
	// Content write after the fact append (the asset-registration ordering
	// precedent): a failed content write leaves a fact whose blob is missing —
	// the download path then fails observably (fixture_content_unavailable) and
	// the agent re-saves; it is never a silent success. Log loudly.
	ref, err := s.content.PutFixture(ctx, written.ID, "storage-state.json", state)
	if err != nil {
		slog.ErrorContext(ctx, "session fixture content write failed after the fact append; re-save the fixture",
			"fixture_id", written.ID, "case_run_id", scope.CaseRunID, "error", err)
		return nil, err
	}
	if ref.SHA256 != written.StorageStateRef {
		return nil, fmt.Errorf("session fixture content address mismatch: ledger %s != fact %s", ref.SHA256, written.StorageStateRef)
	}
	if err := s.session.LinkSessionFixture(ctx, scope, in.SUTID, role, written.ID); err != nil {
		return nil, err
	}
	s.publishSaved(ctx, scope, written)
	return map[string]any{
		"version_id":        platformid.Format(scope.VersionID),
		"case_run_id":       platformid.Format(scope.CaseRunID),
		"fixture_id":        platformid.Format(written.ID),
		"sut_id":            platformid.Format(written.SUTID),
		"environment_id":    platformid.Format(written.EnvironmentID),
		"role":              written.Role,
		"storage_state_ref": written.StorageStateRef,
		"bytes":             len(state),
	}, nil
}

// DownloadFixture injects the CURRENT fixture of the reuse key into the run's
// browser session: derive the newest fact, read its verified content bytes,
// write them into the session workspace and run CLI state-load. On success the
// reuse link is recorded. Every failure is observable and injects nothing:
// no fixture -> not_found; missing/corrupt blob -> fixture_content_unavailable;
// failed state-load -> fixture_load_failed (re-login or re-fetch, never a
// silent stale login state — Review Focus 5).
func (s *SessionFixtureService) DownloadFixture(ctx context.Context, scope tools.Scope, in tools.FixtureDownloadInput) (any, error) {
	if s.fixtures == nil || s.catalog == nil || s.content == nil || s.session == nil {
		return nil, platform.Unavailable("session fixture service is not configured")
	}
	if scope.CaseRunID <= 0 {
		return nil, platform.Invalid("case_run_id", "fixture download requires an execution case_run scope")
	}
	environmentID, err := s.environmentOf(ctx, scope.VersionID, in.SUTID)
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(in.AccountRole)
	path, err := fixtureWorkspacePath(in.Path, DefaultFixtureDownloadPath)
	if err != nil {
		return nil, err
	}
	rows, err := s.fixtures.ListSessionFixtures(ctx, scope.VersionID, in.SUTID, environmentID, normalizeFixtureRole(role))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, platform.NotFound("session fixture")
	}
	// Derived current fixture: the newest append-only fact (latest wins).
	current := rows[len(rows)-1]
	state, err := s.content.Open(ctx, current.StorageStateRef)
	if err != nil {
		slog.WarnContext(ctx, "session fixture content unavailable; the agent must re-login and re-save",
			"fixture_id", current.ID, "case_run_id", scope.CaseRunID, "error", err)
		return nil, fixtureContentUnavailableError()
	}
	if err := s.session.WriteSessionFile(ctx, scope, in.SUTID, role, path, state); err != nil {
		return nil, err
	}
	if err := s.session.LoadBrowserState(ctx, scope, in.SUTID, role, path); err != nil {
		// No reuse link on a failed injection: the run did NOT reuse this fixture.
		return nil, err
	}
	if err := s.session.LinkSessionFixture(ctx, scope, in.SUTID, role, current.ID); err != nil {
		return nil, err
	}
	return map[string]any{
		"version_id":        platformid.Format(scope.VersionID),
		"case_run_id":       platformid.Format(scope.CaseRunID),
		"fixture_id":        platformid.Format(current.ID),
		"sut_id":            platformid.Format(current.SUTID),
		"environment_id":    platformid.Format(current.EnvironmentID),
		"role":              current.Role,
		"storage_state_ref": current.StorageStateRef,
		"path":              path,
		"loaded":            true,
		"saved_at":          current.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// environmentOf resolves the SUT's environment through the version's EFFECTIVE
// catalog (the single effective-scope projection). A SUT outside the effective
// range (another version's, proposed or rejected) is not_found — the fixture
// library never leaks or accepts out-of-scope reuse keys.
func (s *SessionFixtureService) environmentOf(ctx context.Context, versionID, sutID int64) (int64, error) {
	if sutID <= 0 {
		return 0, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	catalog, err := s.catalog.EffectiveSUTCatalog(ctx, versionID)
	if err != nil {
		return 0, err
	}
	for _, sut := range catalog {
		if sut.SUTID == sutID {
			return sut.EnvironmentID, nil
		}
	}
	return 0, platform.NotFound("sut")
}

// publishSaved emits session.saved on the version channel (best-effort; the
// fact is already persisted, the execution publishEvent precedent).
func (s *SessionFixtureService) publishSaved(ctx context.Context, scope tools.Scope, fact execution.SessionFixture) {
	if s.events == nil || scope.VersionID <= 0 {
		return
	}
	payload := map[string]any{
		"case_run_id":       platformid.Format(scope.CaseRunID),
		"fixture_id":        platformid.Format(fact.ID),
		"sut_id":            platformid.Format(fact.SUTID),
		"environment_id":    platformid.Format(fact.EnvironmentID),
		"role":              fact.Role,
		"storage_state_ref": fact.StorageStateRef,
	}
	publishEvent(ctx, s.events, EventSessionSaved, scope.VersionID, map[string]any{
		"case_run_id": platformid.Format(scope.CaseRunID),
		"sut_id":      platformid.Format(fact.SUTID),
	}, payload, nil)
}

// normalizeFixtureRole maps the empty (default-session) role to the stable
// "default" label so the reuse key is always a non-empty role (the workspace
// role-segment precedent). Upload and download normalize identically.
func normalizeFixtureRole(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "default"
	}
	return trimmed
}

// fixtureWorkspacePath validates one workspace-relative file path. The executor
// confines files to the session workspace; the server additionally rejects an
// absolute or escaping path so a fixture can never address another run's files.
func fixtureWorkspacePath(raw, fallback string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	if len(trimmed) > 512 {
		return "", platform.Invalid("path", "must be at most 512 characters")
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") || strings.HasPrefix(trimmed, "~") {
		return "", platform.Invalid("path", "must be a workspace-relative file path")
	}
	for _, segment := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return "", platform.Invalid("path", "must stay inside the session workspace (no .. segments)")
		}
	}
	return trimmed, nil
}

// validateFixtureState checks the saved file is a non-empty storage-state JSON
// object (Playwright storage state carries cookies/origins). A non-state file
// is rejected before any fact/content write — the library never stores garbage
// that would later fail at state-load.
func validateFixtureState(state []byte) error {
	if len(state) == 0 {
		return platform.Invalid("path", "the saved state file is empty; run browser.cli state-save first")
	}
	if len(state) > maxFixtureStateBytes {
		return platform.Invalid("path", "the storage state exceeds the 4MiB transfer bound")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(state, &object); err != nil || object == nil {
		return platform.Invalid("path", "the saved file is not a JSON storage state; run browser.cli state-save first")
	}
	if _, hasCookies := object["cookies"]; !hasCookies {
		if _, hasOrigins := object["origins"]; !hasOrigins {
			return platform.Invalid("path", "the saved file is not a browser storage state (no cookies/origins); run browser.cli state-save first")
		}
	}
	return nil
}

// fixtureLoadFailedError is the observable fail-closed outcome of a failed CLI
// state-load (expired/rejected storage state): a 409 the model can act on —
// re-login and re-save, or retry the download — never a silent stale session.
func fixtureLoadFailedError(detail string) error {
	err := platform.NewError(http.StatusConflict, "fixture_load_failed",
		"the saved login state could not be loaded into the browser session")
	err.Details = map[string]any{
		"reason": detail,
		"hint":   "log in again through browser.cli, save the fresh state with browser.cli state-save + fixture.upload, then retry fixture.download",
	}
	return err
}

// fixtureContentUnavailableError is the observable outcome of a fixture whose
// content blob is missing/corrupt in the ledger: re-login and re-save.
func fixtureContentUnavailableError() error {
	err := platform.NewError(http.StatusConflict, "fixture_content_unavailable",
		"the fixture's stored login state is unavailable")
	err.Details = map[string]any{
		"hint": "log in again through browser.cli and save a fresh fixture with browser.cli state-save + fixture.upload",
	}
	return err
}

// --- CaseRunner session adapter (the FixtureSessionPort implementation) ------
//
// These methods run on the SAME fenced executor session as the execution tools
// (requireRunningScope + bindingFor + ensureSession + recording drain), so a
// fixture save/load is recorded as immutable file/command evidence exactly like
// any other workspace operation.

// fixtureSessionFor resolves the run's active (SUT, account_role) session.
func (s *CaseRunner) fixtureSessionFor(ctx context.Context, scope tools.Scope, sutID int64, role string) (execution.CaseRun, execution.CaseRunSUTContext, error) {
	if s.execution == nil {
		return execution.CaseRun{}, execution.CaseRunSUTContext{}, errors.New("case runner has no execution executor")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return execution.CaseRun{}, execution.CaseRunSUTContext{}, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, sutID)
	if err != nil {
		return execution.CaseRun{}, execution.CaseRunSUTContext{}, err
	}
	session, err := s.ensureSession(ctx, caseRun, binding, role, sessionBaseURL(binding))
	if err != nil {
		return execution.CaseRun{}, execution.CaseRunSUTContext{}, err
	}
	return caseRun, session, nil
}

// ReadSessionFile downloads one workspace file of the run's session (the
// state-save output). A truncated transfer fails explicitly — a partial state
// is never saved as a fixture.
func (s *CaseRunner) ReadSessionFile(ctx context.Context, scope tools.Scope, sutID int64, role, path string) ([]byte, error) {
	caseRun, session, err := s.fixtureSessionFor(ctx, scope, sutID, role)
	if err != nil {
		return nil, err
	}
	outcome, opErr := s.execution.Files(ctx, caseRunExecutorID(caseRun), session.SessionID, FileOperation{
		Op: "download", Path: path,
	}, session.LeaseEpoch)
	if opErr != nil && sessionGone(opErr) {
		s.markSUTContext(ctx, caseRun.ID, sutID, role, execution.SUTContextFailed)
	}
	_, _, drainErr := s.storeExecutionEvidence(ctx, caseRun, session, scope.AgentRunID)
	if opErr != nil {
		return nil, opErr
	}
	if drainErr != nil {
		return nil, drainErr
	}
	if outcome.Truncated {
		return nil, platform.StateConflict("the workspace state file exceeds the transfer bound and was truncated; it cannot be saved as a fixture")
	}
	data, decodeErr := base64.StdEncoding.DecodeString(outcome.Base64)
	if decodeErr != nil {
		return nil, errors.New("executor file content is not valid base64")
	}
	return data, nil
}

// WriteSessionFile uploads the fixture bytes into the run's session workspace
// (the state-load input). The upload settles as file evidence via the drain.
func (s *CaseRunner) WriteSessionFile(ctx context.Context, scope tools.Scope, sutID int64, role, path string, data []byte) error {
	caseRun, session, err := s.fixtureSessionFor(ctx, scope, sutID, role)
	if err != nil {
		return err
	}
	_, opErr := s.execution.Files(ctx, caseRunExecutorID(caseRun), session.SessionID, FileOperation{
		Op: "upload", Path: path, Base64: base64.StdEncoding.EncodeToString(data),
	}, session.LeaseEpoch)
	if opErr != nil && sessionGone(opErr) {
		s.markSUTContext(ctx, caseRun.ID, sutID, role, execution.SUTContextFailed)
	}
	_, _, drainErr := s.storeExecutionEvidence(ctx, caseRun, session, scope.AgentRunID)
	if opErr != nil {
		return opErr
	}
	return drainErr
}

// LoadBrowserState injects the workspace storage-state file through the official
// CLI state-load (the same general-execution path browser.cli uses, so the
// command is recorded as evidence). A non-zero exit / timeout / cancellation is
// a typed fixture_load_failed — the caller must re-login or re-fetch, never
// pretend the session is logged in.
func (s *CaseRunner) LoadBrowserState(ctx context.Context, scope tools.Scope, sutID int64, role, path string) error {
	outcome, err := s.Exec(ctx, scope, ExecCommandInput{
		SUTID:       &sutID,
		AccountRole: role,
		Command:     ExecCommand{Argv: []string{"playwright-cli", "state-load", path}},
	})
	if err != nil {
		return err
	}
	if outcome.TimedOut {
		return fixtureLoadFailedError("state-load timed out")
	}
	if outcome.Cancelled {
		return fixtureLoadFailedError("state-load was cancelled")
	}
	if outcome.Exit == nil || *outcome.Exit != 0 {
		detail := strings.TrimSpace(outcome.Stderr)
		if detail == "" {
			// Safe-dereference: Exit is *int; print the numeric exit code, never
			// the pointer address (or <nil> when the executor reported no exit).
			if outcome.Exit != nil {
				detail = fmt.Sprintf("state-load exited %d", *outcome.Exit)
			} else {
				detail = "state-load did not report an exit code"
			}
		}
		return fixtureLoadFailedError(truncateRunes(detail, 500))
	}
	return nil
}

// LinkSessionFixture records the fixture reference on the run's (SUT,
// account_role) case_run_sut_context row — the cross-case reuse link (SC-F5).
func (s *CaseRunner) LinkSessionFixture(ctx context.Context, scope tools.Scope, sutID int64, role string, fixtureID int64) error {
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return err
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return mapExecutionError(err, "case_run_sut_context")
	}
	for _, row := range rows {
		if row.SUTID != sutID || row.AccountRole != role {
			continue
		}
		rowID := row.ID
		fixture := fixtureID
		ok, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
			target.SessionFixtureID = &fixture
			return nil
		})
		if updateErr != nil {
			return mapExecutionError(updateErr, "case_run_sut_context")
		}
		if !ok {
			return platform.LeaseLost()
		}
		return nil
	}
	return platform.NotFound("case_run_sut_context")
}
