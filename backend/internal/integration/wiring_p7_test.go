//go:build integration

// Package integration_test holds the P7 composition-root smoke test: it starts
// the real API binary against the dedicated test database and asserts that the
// startup seeds and workers are wired. It is skipped unless
// EZTEST_TEST_DATABASE_URL points at a dedicated *_test database (the shared
// testutil helper enforces that).
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Identifiers of the terminal baseline batch seeded before the smoke process
// starts; the autonomy worker must start its failure analysis on the first tick.
const (
	p7SmokeVersionID  = int64(9801)
	p7SmokeBatchID    = int64(98001)
	p7SmokeCaseRunID  = int64(98011)
	p7SmokeCaseNodeID = int64(98021)
)

// p7WiringStartupTimeout bounds the API boot (migrations, seeds, executor probe,
// listener) in the smoke test.
const p7WiringStartupTimeout = 60 * time.Second

// TestP7WiringStartup starts the real composition root against the test database
// and asserts the P7 wiring:
//   - the API answers GET /api/health with 200;
//   - the delivery routes are mounted exactly once (a duplicate ServeMux pattern
//     would panic at boot): the version-scoped issue page, the T2b-guarded issue
//     create path and the T17b global issue/report centres all answer 200/201;
//   - a page with rows carries its version display name (the T17b version-name
//     adapter is bound, not just the empty-page path);
//   - the three P7 profiles (failure analysis, issue triage, supplement) and the
//     execution profile are seeded;
//   - execution-agent allows execution.db_assert and its prompt carries the
//     db-assert rule;
//   - the process keeps running with every worker started (no startup panic).
func TestP7WiringStartup(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	databaseURL := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}

	binary := buildAPIBinary(t)
	port := freePort(t)
	reportDir := t.TempDir()
	env := p7SmokeEnvironment(t, databaseURL, port, reportDir)
	// Seed one terminal baseline batch with a failed(application) case before the
	// process starts: the autonomous worker's first tick must start its failure
	// analysis, which proves the worker chain is started by the real composition
	// root (not just that the process stays alive).
	p7SeedWiringChain(t, pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = env
	var logs syncBuffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start API: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("API process did not exit after kill")
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitAPIHealth(t, baseURL, done, &logs)
	assertP7OpenAPIDocument(t, baseURL)
	token := p7Login(t, baseURL)
	assertP7RoutesMounted(t, baseURL, token)
	assertP7IssueCreateBound(t, baseURL, token)
	assertP7GlobalCentresMounted(t, baseURL, token)

	// The seeds run before the listener, so the health answer proves they already
	// completed. Assert the stored profile rows.
	assertP7Profiles(t, pool)
	assertP7WorkerChainStarted(t, pool)

	// Every worker started: the process is still alive a moment after serving.
	select {
	case err := <-done:
		t.Fatalf("API process exited after startup: %v\n%s", err, logs.String())
	case <-time.After(2 * time.Second):
	}
	if strings.Contains(logs.String(), "panic:") {
		t.Fatalf("API logs contain a panic:\n%s", logs.String())
	}
}

// buildAPIBinary compiles the real API binary into the test temp dir. It uses the
// Go toolchain that compiled the test (runtime.GOROOT) because the repository
// ships a local toolchain that is not necessarily on PATH.
func buildAPIBinary(t *testing.T) string {
	t.Helper()
	backendRoot := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "eztest-smoke")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	if _, err := os.Stat(goTool); err != nil {
		t.Skipf("go toolchain not found at %s: %v", goTool, err)
	}
	build := exec.Command(goTool, "build", "-o", binary, "./cmd/eztest")
	build.Dir = backendRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build API binary: %v\n%s", err, output)
	}
	return binary
}

// p7SmokeEnvironment builds the API environment: the test database, a random
// secret key, temporary storage roots, a debug log and a dedicated HTTP port. It
// strips inherited EZTEST_* variables so a developer shell cannot change the
// smoke run.
func p7SmokeEnvironment(t *testing.T, databaseURL string, port int, reportDir string) []string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate secret key: %v", err)
	}
	secretKeys := fmt.Sprintf(`{"1":%q}`, base64.StdEncoding.EncodeToString(key))
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	openAPIPath := filepath.Join(repoRoot, "docs", "openapi", "api.yaml")

	overrides := map[string]string{
		"EZTEST_ENV":                    "test",
		"EZTEST_DATABASE_URL":           databaseURL,
		"EZTEST_HTTP_ADDR":              fmt.Sprintf("127.0.0.1:%d", port),
		"EZTEST_ADMIN_USERNAME":         "p7-smoke-admin",
		"EZTEST_ADMIN_PASSWORD":         "p7-smoke-password",
		"EZTEST_SECRET_KEYS":            secretKeys,
		"EZTEST_SECRET_KEY_ACTIVE":      "1",
		"EZTEST_STORAGE_DIR":            filepath.Join(reportDir, "objects"),
		"EZTEST_VERSION_ATTACHMENT_DIR": filepath.Join(reportDir, "attachments"),
		"EZTEST_EXPORT_DIR":             filepath.Join(reportDir, "exports"),
		"EZTEST_EVIDENCE_DIR":           filepath.Join(reportDir, "evidence"),
		"EZTEST_REPORT_DIR":             filepath.Join(reportDir, "reports"),
		"EZTEST_OPENAPI_PATH":           openAPIPath,
		"EZTEST_LOG_LEVEL":              "debug",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "EZTEST_") || strings.HasPrefix(entry, "llm_") {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// freePort reserves one loopback port, closes it and returns the number.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// waitAPIHealth polls GET /api/health until it answers 200 or the API exits.
func waitAPIHealth(t *testing.T, baseURL string, done <-chan error, logs *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(p7WiringStartupTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("API exited before becoming healthy: %v\n%s", err, logs.String())
		default:
		}
		response, err := client.Get(baseURL + "/api/health")
		if err == nil {
			status := response.StatusCode
			_ = response.Body.Close()
			if status == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("API did not become healthy within %s\n%s", p7WiringStartupTimeout, logs.String())
}

// assertP7OpenAPIDocument proves EZTEST_OPENAPI_PATH resolves to the repository
// root document. The smoke environment used to point at backend/docs/openapi,
// which does not exist; the endpoint then answered 404 and no P7 route could be
// checked against the contract.
func assertP7OpenAPIDocument(t *testing.T, baseURL string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(baseURL + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("GET /api/openapi.yaml: %v", err)
	}
	defer response.Body.Close()
	document, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read /api/openapi.yaml: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/openapi.yaml = %d, want 200 (the configured path must exist)",
			response.StatusCode)
	}
	if !bytes.Contains(document, []byte("/api/versions/{id}/issues:")) {
		t.Fatal("the served OpenAPI document is missing the P7 issue route")
	}
}

// assertP7RoutesMounted proves the delivery route group is mounted exactly once:
// an unmounted /api/versions/1/issues answers 404, a mounted one renders the
// empty page. A duplicate registration of any mounted pattern would have
// panicked the process in server.New, so a healthy listener already proves the
// no-duplicate-mount rule for every group.
func assertP7RoutesMounted(t *testing.T, baseURL, token string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/versions/1/issues", nil)
	if err != nil {
		t.Fatalf("build issue request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET /api/versions/1/issues: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read issue page: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/versions/1/issues = %d, want 200 (route mounted): %s",
			response.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"data"`)) {
		t.Fatalf("issue page=%s want the data envelope", body)
	}
}

// assertP7IssueCreateBound proves the T2b version reader is bound in the real
// composition root: creating a human issue on the seeded version commits (201)
// instead of failing closed with the "issue version reader is not configured"
// HTTP 500.
func assertP7IssueCreateBound(t *testing.T, baseURL, token string) {
	t.Helper()
	body := `{"title":"p7 smoke issue create","severity":"major","category":"defect","description":"composition root smoke"}`
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/versions/9801/issues",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build issue create request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("POST /api/versions/9801/issues: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read issue create response: %v", err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/versions/9801/issues = %d, want 201 (version reader bound): %s",
			response.StatusCode, raw)
	}
	var envelope struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode issue create response: %v body=%s", err, raw)
	}
	if envelope.Data.ID == "" || envelope.Data.Status != "open" {
		t.Fatalf("created issue=%+v want a committed open issue", envelope.Data)
	}
}

// assertP7GlobalCentresMounted proves the T17b global centres are mounted and
// bound in the real composition root: GET /api/issues and GET /api/reports
// answer 200, and the issue page carries the smoke issue with its version
// display name (a page with rows fails closed without the version-name adapter).
func assertP7GlobalCentresMounted(t *testing.T, baseURL, token string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/api/issues?limit=100", "/api/reports?limit=100"} {
		request, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			t.Fatalf("build %s request: %v", path, err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (global centre mounted): %s",
				path, response.StatusCode, raw)
		}
		if !bytes.Contains(raw, []byte(`"data"`)) {
			t.Fatalf("GET %s body=%s want the page envelope", path, raw)
		}
	}

	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/issues?limit=100&q=p7+smoke+issue+create", nil)
	if err != nil {
		t.Fatalf("build global issue query: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET the global issue page: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the global issue page: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET the global issue page = %d, want 200: %s", response.StatusCode, raw)
	}
	var page struct {
		Data []struct {
			Title       string `json:"title"`
			VersionName string `json:"versionName"`
			DeepLink    struct {
				VersionID string `json:"versionId"`
				Tab       string `json:"tab"`
			} `json:"deepLink"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode the global issue page: %v body=%s", err, raw)
	}
	for _, row := range page.Data {
		if row.Title != "p7 smoke issue create" {
			continue
		}
		if row.VersionName != "p7-wiring-version" {
			t.Fatalf("global issue versionName=%q want p7-wiring-version (version-name adapter bound)",
				row.VersionName)
		}
		if row.DeepLink.VersionID != "9801" || row.DeepLink.Tab != "issues" {
			t.Fatalf("global issue deepLink=%+v want version 9801/issues", row.DeepLink)
		}
		return
	}
	t.Fatalf("the created smoke issue is missing from the global issue page: %s", raw)
}

// p7Login authenticates the seeded smoke administrator and returns its bearer
// token.
func p7Login(t *testing.T, baseURL string) string {
	t.Helper()
	body := strings.NewReader(`{"username":"p7-smoke-admin","password":"p7-smoke-password"}`)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		baseURL+"/api/auth/login", "application/json", body)
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if response.StatusCode != http.StatusOK || envelope.Data.Token == "" {
		t.Fatalf("login = %d token empty=%v", response.StatusCode, envelope.Data.Token == "")
	}
	return envelope.Data.Token
}

// p7SeedWiringChain inserts the executing version, terminal baseline batch and
// failed(application) case run the startup worker must analyze.
func p7SeedWiringChain(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	p7SeedVersion(t, pool, p7SmokeVersionID, "executing", "", false)
	p7SeedBatch(t, pool, p7SmokeBatchID, p7SmokeVersionID, 1, "done", "baseline", nil, false)
	p7SeedCaseRun(t, pool, p7SmokeCaseRunID, p7SmokeBatchID, p7SmokeVersionID,
		p7SmokeCaseNodeID, 1, "failed", "application", "Smoke checkout")
}

// assertP7WorkerChainStarted proves the real composition root started the
// autonomy worker: its first tick starts the failure analysis of the seeded
// terminal batch within a few seconds. Without the worker wiring this row would
// never appear.
func assertP7WorkerChainStarted(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var status string
		err := pool.QueryRow(context.Background(), `
			SELECT status FROM analysis_run
			WHERE batch_id = $1 AND kind = 'failure'
			ORDER BY id DESC LIMIT 1`, p7SmokeBatchID).Scan(&status)
		if err == nil {
			if status == "" {
				t.Fatal("the autonomy worker created a failure analysis without a status")
			}
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read the worker failure analysis: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the autonomy worker did not start the failure analysis within 20s")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// assertP7Profiles checks the four seeded profiles of the P7 wiring.
func assertP7Profiles(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	required := []string{
		"failure-analysis-agent",
		"issue-triage-agent",
		"supplement-agent",
		"execution-agent",
	}
	rows, err := pool.Query(ctx,
		`SELECT key, allowed_tools, system_prompt FROM agent_profile WHERE key = ANY($1)`,
		required)
	if err != nil {
		t.Fatalf("read seeded profiles: %v", err)
	}
	defer rows.Close()

	found := make(map[string]struct{}, len(required))
	for rows.Next() {
		var (
			key    string
			tools  []string
			prompt string
		)
		if err := rows.Scan(&key, &tools, &prompt); err != nil {
			t.Fatalf("scan seeded profile: %v", err)
		}
		found[key] = struct{}{}
		if len(tools) == 0 {
			t.Fatalf("profile %q allows no tools", key)
		}
		if key == "execution-agent" {
			if !containsString(tools, "execution.db_assert") {
				t.Fatalf("execution-agent tools=%v want execution.db_assert", tools)
			}
			if !strings.Contains(prompt, "execution.db_assert") {
				t.Fatal("execution-agent prompt is missing the db-assert rule")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read seeded profiles: %v", err)
	}
	for _, key := range required {
		if _, ok := found[key]; !ok {
			t.Fatalf("profile %q was not seeded (found %v)", key, found)
		}
	}
}

// containsString reports whether values contains want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// syncBuffer is a concurrency-safe log buffer: the API writes while the test
// polls for health.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the buffer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns the buffered log text.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
