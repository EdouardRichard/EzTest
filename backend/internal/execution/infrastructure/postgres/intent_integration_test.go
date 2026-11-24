//go:build integration

package executionpg

// R-G9 (v0.2.4-G T3, SC-G12) lock-atomic ordering proof: inside the intent
// insert transaction, under the version advisory lock, dedup/conflicts resolve
// BEFORE the completed-version reopen signal. A duplicate or conflicting
// intent must never flip a completed version to running; a genuinely new
// retest on a completed version must still reopen+create (the incremental
// retest chain stays reachable), and the reopen-and-retry loop must stay
// reachable for a close that commits between the service pre-check and the
// insert.
//
// These tests drive the REAL IntentRepository (the guard ordering under test)
// and the REAL version repository ReopenIfCompleted CAS (the single reopen
// terminal entry); only the version read projection and the design case read
// are faked, because their production adapters need the full version/design
// assembly. The fake reopen port records calls AND delegates to the real CAS,
// so "was the version reopened" is asserted both as a call count and as the
// persisted status.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

const intentOrderCaseVersionID = int64(9301)

// intentOrderVersionPort implements executionapp.VersionReader over the real
// version row: Get reads the persisted status (so a retry sees a committed
// close/reopen), Reopen delegates to the real ReopenIfCompleted CAS and records
// the call.
type intentOrderVersionPort struct {
	pool     *pgxpool.Pool
	versions *versionpg.Repository
	template executionapp.VersionBinding
	mu       sync.Mutex
	reopens  []int64
}

func (p *intentOrderVersionPort) Get(ctx context.Context, id int64) (executionapp.VersionBinding, error) {
	var status string
	if err := p.pool.QueryRow(ctx, `SELECT status FROM version WHERE id = $1`, id).Scan(&status); err != nil {
		return executionapp.VersionBinding{}, err
	}
	binding := p.template
	binding.VersionID = id
	binding.Status = status
	return binding, nil
}

func (p *intentOrderVersionPort) Reopen(ctx context.Context, id int64) error {
	p.mu.Lock()
	p.reopens = append(p.reopens, id)
	p.mu.Unlock()
	_, _, err := p.versions.ReopenIfCompleted(ctx, id, time.Now().UTC())
	return err
}

func (p *intentOrderVersionPort) reopenCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reopens)
}

// intentOrderCaseReader serves the one confirmed case snapshot the manual
// retest path reads. Methods the tested paths must not call panic instead of
// supplying a fiat zero value (method discipline 9).
type intentOrderCaseReader struct{ snapshot execution.CaseSnapshot }

func (r *intentOrderCaseReader) GetCaseVersion(_ context.Context, _, caseVersionID int64) (execution.CaseSnapshot, error) {
	if caseVersionID != r.snapshot.CaseVersionID {
		return execution.CaseSnapshot{}, execution.ErrNotFound
	}
	return r.snapshot, nil
}

func (r *intentOrderCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	panic("intent order tests must not list executable cases")
}

func (r *intentOrderCaseReader) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	panic("intent order tests must not resolve replacements")
}

func (r *intentOrderCaseReader) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	panic("intent order tests must not read case version labels")
}

type intentOrderFixture struct {
	repo        *Repository
	pool        *pgxpool.Pool
	ids         platformid.Generator
	versions    *intentOrderVersionPort
	service     *executionapp.IntentService
	versionID   int64
	fingerprint string
	baseline    string
}

func newIntentOrderFixture(t *testing.T, status string) *intentOrderFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	repo := NewRepository(pool, ids)
	versionID := ids.Next()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, status, created_by, environment_id)
		VALUES ($1, 'reopen order', $2, 1, 9901)
	`, versionID, status); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	template := executionapp.VersionBinding{
		VersionID: versionID,
		Stage:     string(versiondomain.StageExecuting),
		SUTs: []executionapp.SUTBinding{{
			SUTID: 5001, EnvironmentID: 7001,
			SUTName: "sut", EnvironmentName: "env", BaseURL: "http://127.0.0.1:13390",
			PageIDs: []int64{}, APIIDs: []int64{}, DatabaseIDs: []int64{}, AccountRoles: []string{"user"},
		}},
	}
	fingerprint, err := executionapp.EnvironmentFingerprint(template)
	if err != nil {
		t.Fatalf("environment fingerprint: %v", err)
	}
	baseline, err := executionapp.CodeBaseline(template)
	if err != nil {
		t.Fatalf("code baseline: %v", err)
	}
	versions := &intentOrderVersionPort{pool: pool, versions: versionpg.New(pool, ids), template: template}
	snapshot := execution.CaseSnapshot{
		CaseID: 9300, CaseVersionID: intentOrderCaseVersionID,
		RequirementID: 9501, RequirementVersionID: 9601,
		Title: "order case", Priority: "P2",
		Steps:      []execution.CaseStep{{Seq: 1, Action: "step", TargetDescription: "target"}},
		Assertions: []execution.CaseAssertion{{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "ok", SUTID: 5001}},
	}
	service := executionapp.NewIntentService(executionapp.IntentServiceDeps{
		Intents:  repo.Intents,
		Cases:    &intentOrderCaseReader{snapshot: snapshot},
		Versions: versions,
		IDs:      ids,
	})
	return &intentOrderFixture{
		repo: repo, pool: pool, ids: ids, versions: versions, service: service,
		versionID: versionID, fingerprint: fingerprint, baseline: baseline,
	}
}

func (f *intentOrderFixture) newIntent(purpose execution.Purpose) execution.ExecutionIntent {
	now := time.Now().UTC()
	return execution.ExecutionIntent{
		ID:                     f.ids.Next(),
		VersionID:              f.versionID,
		CaseVersionID:          intentOrderCaseVersionID,
		SUTIDs:                 []int64{5001},
		EnvironmentFingerprint: f.fingerprint,
		CodeBaseline:           f.baseline,
		Purpose:                purpose,
		Status:                 execution.IntentOpen,
		DedupeKey:              "order-test",
		AttemptNo:              1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
}

func (f *intentOrderFixture) setStatus(t *testing.T, status string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE version SET status = $2 WHERE id = $1`, f.versionID, status); err != nil {
		t.Fatalf("set version status: %v", err)
	}
}

func (f *intentOrderFixture) status(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM version WHERE id = $1`, f.versionID).Scan(&status); err != nil {
		t.Fatalf("read version status: %v", err)
	}
	return status
}

func (f *intentOrderFixture) activeIntents(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM execution_intent
		WHERE version_id = $1 AND status = ANY($2::text[])
	`, f.versionID, intentActiveStatuses).Scan(&count); err != nil {
		t.Fatalf("count active intents: %v", err)
	}
	return count
}

// waitForIntentAdvisoryWaiter blocks until some session is queued on an
// advisory lock, proving the intent transaction really waits for the racing
// transaction instead of overtaking it.
func waitForIntentAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
		`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("intent creation never blocked on the version advisory lock")
}

// A duplicate retest on a completed version resolves as a conflict WITHOUT
// reopening: at the repository the dedup pre-check must answer ErrDuplicate
// before the completed-version guard can answer ErrVersionCompleted (which the
// service loop would otherwise answer with Reopen), and through the service the
// manual API returns state_conflict with zero reopen calls.
func TestDuplicateRetestOnCompletedDoesNotReopen(t *testing.T) {
	f := newIntentOrderFixture(t, versiondomain.StatusRunning)
	ctx := context.Background()
	if err := f.repo.Intents.Create(ctx, f.newIntent(execution.PurposeRetest)); err != nil {
		t.Fatalf("seed active retest intent: %v", err)
	}
	f.setStatus(t, versiondomain.StatusCompleted)

	// Repository contract: dedup before the reopen signal, under the lock.
	err := f.repo.Intents.Create(ctx, f.newIntent(execution.PurposeRetest))
	if !errors.Is(err, execution.ErrDuplicate) {
		t.Fatalf("duplicate insert on completed version = %v, want ErrDuplicate (dedup must resolve before the reopen signal)", err)
	}
	if got := f.status(t); got != versiondomain.StatusCompleted {
		t.Fatalf("version status after duplicate insert = %q, want completed", got)
	}

	// Service contract: the manual retest API returns the conflict and never
	// calls the reopen port.
	_, err = f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeRetest, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("duplicate CreateManual = %v, want state_conflict", err)
	}
	if calls := f.versions.reopenCount(); calls != 0 {
		t.Fatalf("duplicate retest called Reopen %d time(s), want 0", calls)
	}
	if got := f.status(t); got != versiondomain.StatusCompleted {
		t.Fatalf("version status after duplicate retest = %q, want completed", got)
	}
	if active := f.activeIntents(t); active != 1 {
		t.Fatalf("active intents = %d, want only the seeded one", active)
	}
}

// A genuinely NEW retest on a completed version still reopens and creates:
// the incremental retest chain stays reachable after the ordering fix.
func TestGenuineNewRetestReopensAndCreates(t *testing.T) {
	f := newIntentOrderFixture(t, versiondomain.StatusCompleted)
	ctx := context.Background()

	intent, err := f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeRetest, 42)
	if err != nil {
		t.Fatalf("genuine new retest on completed version: %v", err)
	}
	if got := f.status(t); got != versiondomain.StatusRunning {
		t.Fatalf("version status = %q, want running (reopened)", got)
	}
	if calls := f.versions.reopenCount(); calls != 1 {
		t.Fatalf("Reopen calls = %d, want exactly 1", calls)
	}
	stored, err := f.repo.Intents.Get(ctx, intent.ID)
	if err != nil || stored.Status != execution.IntentOpen {
		t.Fatalf("stored intent = %+v err=%v, want open", stored, err)
	}

	// A second genuine intent (different natural key) on the now-running
	// version creates without another reopen.
	if _, err := f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeManual, 42); err != nil {
		t.Fatalf("second genuine intent on running version: %v", err)
	}
	if calls := f.versions.reopenCount(); calls != 1 {
		t.Fatalf("Reopen calls after second create = %d, want still 1", calls)
	}
	if active := f.activeIntents(t); active != 2 {
		t.Fatalf("active intents = %d, want 2", active)
	}
}

// The reopen-and-retry loop stays reachable for the concurrent-close race: a
// close that commits while the insert waits on the version advisory lock is
// answered with reopen+retry, and the intent lands on a running version.
func TestConcurrentCloseRaceReopenRetry(t *testing.T) {
	f := newIntentOrderFixture(t, versiondomain.StatusRunning)
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin racing close: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockVersionTx(ctx, tx, f.versionID, "racing close"); err != nil {
		t.Fatalf("lock version: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE version SET status = $2 WHERE id = $1`, f.versionID, versiondomain.StatusCompleted); err != nil {
		t.Fatalf("racing close update: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, createErr := f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeRetest, 42)
		result <- createErr
	}()
	waitForIntentAdvisoryWaiter(t, f.pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit racing close: %v", err)
	}

	if err := <-result; err != nil {
		t.Fatalf("CreateManual after losing the close race: %v", err)
	}
	if got := f.status(t); got != versiondomain.StatusRunning {
		t.Fatalf("version status = %q, want running (reopened by the retry loop)", got)
	}
	if calls := f.versions.reopenCount(); calls < 1 {
		t.Fatalf("Reopen calls = %d, want >= 1 (the retry loop must stay reachable)", calls)
	}
	if active := f.activeIntents(t); active != 1 {
		t.Fatalf("active intents = %d, want 1", active)
	}
}

// The decisive race window: a racing transaction commits BOTH the duplicate
// natural key and the version close while the retest insert waits on the
// advisory lock. When the insert acquires the lock it must see the duplicate
// first and return the conflict without reopening the completed version.
func TestConcurrentDuplicateCloseDoesNotReopen(t *testing.T) {
	f := newIntentOrderFixture(t, versiondomain.StatusRunning)
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin racing transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockVersionTx(ctx, tx, f.versionID, "racing duplicate+close"); err != nil {
		t.Fatalf("lock version: %v", err)
	}
	racing := f.newIntent(execution.PurposeRetest)
	if _, err := tx.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
			environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no)
		VALUES ($1, $2, $3, '[5001]'::jsonb, $4, $5, $6, $7, 'race-dup', 1)
	`, racing.ID, racing.VersionID, racing.CaseVersionID, racing.EnvironmentFingerprint,
		racing.CodeBaseline, string(racing.Purpose), string(racing.Status)); err != nil {
		t.Fatalf("racing duplicate insert: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE version SET status = $2 WHERE id = $1`, f.versionID, versiondomain.StatusCompleted); err != nil {
		t.Fatalf("racing close update: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, createErr := f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeRetest, 42)
		result <- createErr
	}()
	waitForIntentAdvisoryWaiter(t, f.pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit racing transaction: %v", err)
	}

	err = <-result
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("duplicate retest after racing close = %v, want state_conflict", err)
	}
	if calls := f.versions.reopenCount(); calls != 0 {
		t.Fatalf("duplicate retest called Reopen %d time(s), want 0", calls)
	}
	if got := f.status(t); got != versiondomain.StatusCompleted {
		t.Fatalf("version status = %q, want completed (the duplicate must not reopen)", got)
	}
	if active := f.activeIntents(t); active != 1 {
		t.Fatalf("active intents = %d, want only the racing one", active)
	}
}

// A closed requirement is a conflict, not a reopen trigger: on a completed
// version (whose requirements are normally closed) a new retest must return
// the actionable requirement_closed conflict WITHOUT reopening the version.
// The requirement guard therefore resolves before the completed-version guard.
func TestClosedRequirementOnCompletedDoesNotReopen(t *testing.T) {
	f := newIntentOrderFixture(t, versiondomain.StatusCompleted)
	ctx := context.Background()
	requirementID := f.ids.Next()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, status) VALUES ($1, $2, $3)
	`, requirementID, f.versionID, string(versiondomain.RequirementClosed)); err != nil {
		t.Fatalf("seed closed requirement: %v", err)
	}
	insertOrderCase(t, f.pool, f.ids, f.versionID, requirementID)

	// Repository contract: the closed-requirement conflict resolves before the
	// completed-version reopen signal.
	err := f.repo.Intents.Create(ctx, f.newIntent(execution.PurposeRetest))
	if !errors.Is(err, execution.ErrRequirementClosed) {
		t.Fatalf("insert against closed requirement on completed version = %v, want ErrRequirementClosed", err)
	}

	// Service contract: actionable 409, zero reopen calls, version completed.
	_, err = f.service.CreateManual(ctx, f.versionID, intentOrderCaseVersionID, execution.PurposeRetest, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("CreateManual against closed requirement = %v, want state_conflict", err)
	}
	if apiErr.Details["reason"] != "requirement_closed" {
		t.Fatalf("details.reason = %#v, want requirement_closed", apiErr.Details["reason"])
	}
	if calls := f.versions.reopenCount(); calls != 0 {
		t.Fatalf("closed-requirement retest called Reopen %d time(s), want 0", calls)
	}
	if got := f.status(t); got != versiondomain.StatusCompleted {
		t.Fatalf("version status = %q, want completed", got)
	}
	if active := f.activeIntents(t); active != 0 {
		t.Fatalf("active intents = %d, want 0", active)
	}
}

// insertOrderCase writes the minimal design rows that bind the fixture case
// version to the requirement so the intent guard can resolve the requirement
// state (same shape as version/infrastructure's insertRaceCase).
func insertOrderCase(t *testing.T, pool *pgxpool.Pool, ids platformid.Generator, versionID, requirementID int64) {
	t.Helper()
	ctx := context.Background()
	scenarioID := ids.Next()
	caseID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, $3, $4, 'order scenario')
	`, scenarioID, versionID, requirementID, ids.Next()); err != nil {
		t.Fatalf("insert case scenario: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, 'confirmed')
	`, caseID, scenarioID, intentOrderCaseVersionID); err != nil {
		t.Fatalf("insert case entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
		VALUES ($1, $2, 1, 'order case', $3, 'confirmed')
	`, intentOrderCaseVersionID, caseID, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert case version: %v", err)
	}
}
