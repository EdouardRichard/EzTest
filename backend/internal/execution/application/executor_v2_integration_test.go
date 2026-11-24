//go:build integration

package executionapp_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// executorProcess is a REAL executor process (node src/server.mjs) started on an
// ephemeral loopback port with its own token, workspace root and state dir.
type executorProcess struct {
	endpoint string
	token    string
	cmd      *exec.Cmd
}

// repoRoot walks up from this file to the repository root (the worktree).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the test file path")
	}
	// .../backend/internal/execution/application/<file>
	dir := filepath.Dir(file)
	for i := 0; i < 4; i++ {
		dir = filepath.Dir(dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "executor", "src", "server.mjs")); err != nil {
		t.Fatalf("executor entry point not found under %s: %v", dir, err)
	}
	return dir
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func startExecutorProcess(t *testing.T) *executorProcess {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the real-executor end-to-end test cannot run")
	}
	root := repoRoot(t)
	entry := filepath.Join(root, "executor", "src", "server.mjs")
	if _, err := os.Stat(filepath.Join(root, "executor", "node_modules")); err != nil {
		t.Skip("executor/node_modules is missing; install executor dependencies first")
	}
	port := freePort(t)
	token := "integration-executor-token"
	stateDir := filepath.Join(t.TempDir(), "state")
	cmd := exec.Command(node, entry)
	cmd.Dir = filepath.Join(root, "executor")
	cmd.Env = append(os.Environ(),
		"EZTEST_EXECUTOR_TOKEN="+token,
		fmt.Sprintf("EZTEST_EXECUTOR_ADDR=127.0.0.1:%d", port),
		"EZTEST_EXECUTOR_STATE_DIR="+stateDir,
		"EZTEST_EXECUTOR_RECLAIM_INTERVAL_MS=0",
		// No EZTEST_API_URL: the pull-only mode keeps the test off the API push routes.
		"EZTEST_API_URL=",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start executor: %v", err)
	}
	logs := make([]string, 0, 64)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			logs = append(logs, scanner.Text())
		}
	}()
	proc := &executorProcess{endpoint: fmt.Sprintf("http://127.0.0.1:%d", port), token: token, cmd: cmd}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(30 * time.Second)
	for {
		response, err := http.Get(proc.endpoint + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return proc
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("executor did not become healthy at %s; stderr:\n%s", proc.endpoint, strings.Join(logs, "\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// integrationSettings serves the content root and the execution knobs from
// memory (the real settings service is not part of this chain).
type integrationSettings struct {
	ints    map[string]int64
	strings map[string]string
}

func (s *integrationSettings) String(_ context.Context, key string) (string, error) {
	if value, ok := s.strings[key]; ok {
		return value, nil
	}
	return "", nil
}

func (s *integrationSettings) Int(_ context.Context, key string) (int64, error) {
	return s.ints[key], nil
}

func (s *integrationSettings) Bool(context.Context, string) (bool, error) { return false, nil }

type integrationClock struct{}

func (integrationClock) Now() time.Time { return time.Now().UTC() }

// integrationSUTContexts mirrors the composition-root adapter that maps the
// postgres store's CreateSUTContext verb onto the narrow application port.
type integrationSUTContexts struct {
	repo *executionpg.SUTContextRepository
}

func (a integrationSUTContexts) Create(ctx context.Context, item execution.CaseRunSUTContext) error {
	return a.repo.CreateSUTContext(ctx, item)
}

func (a integrationSUTContexts) ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListByCaseRun(ctx, caseRunID)
}

func (a integrationSUTContexts) UpdateCAS(ctx context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error) {
	return a.repo.UpdateCAS(ctx, id, mutate)
}

func (a integrationSUTContexts) ListStale(ctx context.Context, createdBefore time.Time, limit int) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListStale(ctx, createdBefore, limit)
}

func (a integrationSUTContexts) ListAll(ctx context.Context) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListAll(ctx)
}

// TestV2EndToEndCommandEvidence is the W3 keystone acceptance: a REAL executor
// process + the v2 Go client + the execution settlement chain persist a command
// recording as content-addressed evidence, and the session is closed afterwards.
//
//	CreateSession → Exec → command artifact → evidence row (sha256 recomputed
//	against the stored bytes) → CloseSession
func TestV2EndToEndCommandEvidence(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	repo := executionpg.NewRepository(pool, ids)
	ctx := context.Background()

	dataRoot := t.TempDir()
	settings := &integrationSettings{
		ints: map[string]int64{
			"execution.evidence_max_bytes":            5 << 20,
			"execution.evidence_max_per_case_run":     0,
			"execution.executor_context_ttl_seconds":  300,
			"execution.case_timeout_seconds":          0,
			"execution.human_request_timeout_seconds": 600,
		},
		strings: map[string]string{"paths.data_root": dataRoot},
	}
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settings, ids, nil, nil)
	content := executionadapters.NewContentStore(storageSvc)

	proc := startExecutorProcess(t)
	client := executorinfra.NewClient(proc.endpoint, proc.token, executorinfra.DefaultTimeout)
	router := executionadapters.NewExecutorRouter(
		executorinfra.NewResolver(proc.token, executorinfra.DefaultTimeout), nil, proc.endpoint)
	adapter := executionadapters.NewExecutionAdapterWithRouter(router)

	// Seed the dispatch chain the settlement path fences against.
	const versionID, sutID, caseVersionID = int64(10), int64(7), int64(41)
	intentID, waveID, caseRunID := ids.Next(), ids.Next(), ids.Next()
	fingerprint := strings.Repeat("a", 64)
	if err := repo.Intents.Create(ctx, execution.ExecutionIntent{
		ID: intentID, VersionID: versionID, CaseVersionID: caseVersionID,
		SUTIDs: []int64{sutID}, EnvironmentFingerprint: fingerprint,
		Purpose: execution.PurposeInitial, Status: execution.IntentRunning,
		DedupeKey: "exec:v2-end-to-end", AttemptNo: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, execution.RunWave{
		ID: waveID, VersionID: versionID, Status: execution.WaveRunning,
		Trigger: execution.WaveTriggerAuto, Counts: execution.WaveCounts{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil, nil); err != nil {
		t.Fatalf("create wave: %v", err)
	}
	if err := repo.CaseRuns.Create(ctx, execution.CaseRun{
		ID: caseRunID, WaveID: waveID, IntentID: intentID, CaseVersionID: caseVersionID,
		Status: execution.CaseRunRunning, Attempts: 1,
		DispatchEpoch: 3, DispatchToken: "dispatch-token",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create case run: %v", err)
	}

	runner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:     repo.Intents,
		CaseRuns:    repo.CaseRuns,
		SUTContexts: integrationSUTContexts{repo: repo.SUTContexts},
		Results:     repo.Results,
		Evidence:    repo.Evidence,
		Content:     content,
		Execution:   adapter,
		Bindings: &fakeBindingResolver{bindings: []execution.SUTBinding{{
			SUTID: sutID, SUTName: "orders", EnvironmentName: "test",
			BaseURL: "http://127.0.0.1:13335", AccountRoles: []string{"admin"},
		}}},
		Settings:   settings,
		Clock:      integrationClock{},
		IDs:        ids,
		InstanceID: "api-integration",
	})
	scope := tools.Scope{
		VersionID: versionID, WaveID: waveID, CaseRunID: caseRunID, IntentID: intentID,
		CaseVersionID: caseVersionID, DispatchEpoch: 3, DispatchToken: "dispatch-token",
		AgentRunID: ids.Next(),
	}

	// Exec through the general execution path: the session is created lazily and
	// the command recording settles as evidence in the same call.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	outcome, err := runner.Exec(ctx, scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command: executionapp.ExecCommand{
			Argv: []string{node, "-e", "process.stdout.write('hello eztest')"},
		},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if outcome.Exit == nil || *outcome.Exit != 0 {
		t.Fatalf("exit = %v, want 0 (outcome %+v)", outcome.Exit, outcome)
	}
	if !strings.Contains(outcome.Stdout, "hello eztest") {
		t.Fatalf("stdout = %q, want it to contain the command output", outcome.Stdout)
	}
	if outcome.SessionID == "" || outcome.CommandID == "" {
		t.Fatalf("outcome carries no session/command identity: %+v", outcome)
	}
	if len(outcome.EvidenceIDs) == 0 {
		t.Fatalf("the command produced no evidence: %+v", outcome)
	}
	if outcome.EvidenceSkipped != 0 {
		t.Fatalf("evidence was dropped under an unlimited cap: %+v", outcome)
	}

	// The session is a real v2 session on the executor, keyed for reconciliation.
	sessions, err := client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != outcome.SessionID {
		t.Fatalf("executor sessions = %+v, want the one session %q", sessions, outcome.SessionID)
	}
	if want := executorinfra.SessionKey(caseRunID, sutID, "admin"); sessions[0].Key != want {
		t.Fatalf("session key = %q, want %q", sessions[0].Key, want)
	}
	// The workspace anchor lives under the configured content root.
	if !strings.HasPrefix(sessions[0].Workspace, dataRoot) {
		t.Fatalf("session workspace = %q, want it under %q", sessions[0].Workspace, dataRoot)
	}

	// The recording ledger carries the command artifact.
	page, err := client.Artifacts(ctx, outcome.SessionID, 0)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(page.Artifacts) == 0 {
		t.Fatal("the executor recorded no artifacts")
	}
	var commandArtifact *executorinfra.Artifact
	for i := range page.Artifacts {
		if page.Artifacts[i].Kind == "command" {
			commandArtifact = &page.Artifacts[i]
			break
		}
	}
	if commandArtifact == nil {
		t.Fatalf("no command artifact in %+v", page.Artifacts)
	}

	// Settlement: the evidence row is in PostgreSQL, its kind is the T6 command
	// kind, and its sha256 matches a RECOMPUTED digest of the stored bytes.
	rows, err := repo.Evidence.ListByCaseRun(ctx, caseRunID, 50)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no evidence row was persisted")
	}
	var commandEvidence *execution.Evidence
	for i := range rows {
		if rows[i].Kind == execution.EvidenceCommand {
			commandEvidence = &rows[i]
			break
		}
	}
	if commandEvidence == nil {
		t.Fatalf("no command evidence row in %+v", rows)
	}
	stored, err := content.Open(ctx, commandEvidence.SHA256)
	if err != nil {
		t.Fatalf("read stored evidence: %v", err)
	}
	sum := sha256.Sum256(stored)
	if recomputed := hex.EncodeToString(sum[:]); recomputed != commandEvidence.SHA256 {
		t.Fatalf("stored sha256 = %q, recomputed %q", commandEvidence.SHA256, recomputed)
	}
	if int64(len(stored)) != commandEvidence.SizeBytes {
		t.Fatalf("stored size = %d, evidence size = %d", len(stored), commandEvidence.SizeBytes)
	}
	if commandEvidence.SUTID == nil || *commandEvidence.SUTID != sutID {
		t.Fatalf("evidence sut id = %v, want %d", commandEvidence.SUTID, sutID)
	}
	// The stored bytes are the executor's command record: argv/env/exit/stdio.
	var record map[string]any
	if err := json.Unmarshal(stored, &record); err != nil {
		t.Fatalf("command evidence is not the recorded JSON: %v", err)
	}
	if record["command_id"] != outcome.CommandID {
		t.Fatalf("recorded command_id = %v, want %q", record["command_id"], outcome.CommandID)
	}
	if record["outcome"] != "exited" {
		t.Fatalf("recorded outcome = %v, want exited", record["outcome"])
	}
	if _, ok := record["env"].(map[string]any); !ok {
		t.Fatalf("the command record must carry the FULL environment (GD-01): %v", record["env"])
	}
	if stdout, _ := record["stdout"].(string); !strings.Contains(stdout, "hello eztest") {
		t.Fatalf("recorded stdout = %q", stdout)
	}

	// The settlement cursor is durable: a second drain settles nothing new.
	second, err := runner.Exec(ctx, scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "0"}},
	})
	if err != nil {
		t.Fatalf("second Exec: %v", err)
	}
	if second.SessionID != outcome.SessionID {
		t.Fatalf("second command created a new session %q (want reuse of %q)", second.SessionID, outcome.SessionID)
	}
	if len(second.EvidenceIDs) != 1 {
		t.Fatalf("second command settled %d evidence rows, want exactly its own record", len(second.EvidenceIDs))
	}
	all, err := repo.Evidence.ListByCaseRun(ctx, caseRunID, 50)
	if err != nil {
		t.Fatalf("ListByCaseRun after the second command: %v", err)
	}
	if len(all) != len(rows)+1 {
		t.Fatalf("evidence rows = %d, want %d (no re-settlement of the first command)", len(all), len(rows)+1)
	}

	// CloseSession: the executor forgets the session and its key.
	if err := client.CloseSession(ctx, outcome.SessionID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	remaining, err := client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after close: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("sessions after close = %+v, want none", remaining)
	}
	if _, err := client.GetSession(ctx, outcome.SessionID); err == nil {
		t.Fatal("the closed session must be gone")
	}
}
