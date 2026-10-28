//go:build integration

// Package integration_test holds the P7 end-to-end integration tests that run
// against a real PostgreSQL database: the delivery HTTP surface wired to real
// repositories/services/handlers, the version continuing/finalize lifecycle and
// the autonomous batch chain, and the execution.db_assert tool against a real
// read-only PostgreSQL target.
//
// These files are add-only Task 26 coverage; the T17a smoke tests in the same
// package are untouched. Every test skips cleanly when EZTEST_TEST_DATABASE_URL
// is unset (testutil.OpenTestDB enforces the dedicated *_test database rule).
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p7xTestTime is the deterministic wall clock shared by the Task 26 fixtures.
// It sits in the past so rows created with the real wall clock (the autonomy
// restart path) sort after the fixture rows.
var p7xTestTime = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

// p7xIDs is a monotonic in-process id generator: the delivery/version services
// mint every id in the application layer (D29), so tests can reserve disjoint
// high ranges per scenario.
type p7xIDs struct{ next atomic.Int64 }

// p7xNewIDs returns a generator starting at start+1.
func p7xNewIDs(start int64) *p7xIDs {
	ids := &p7xIDs{}
	ids.next.Store(start)
	return ids
}

// Next implements the application IDGenerator ports.
func (g *p7xIDs) Next() int64 { return g.next.Add(1) }

// p7xClock is a fixed application clock.
type p7xClock struct{ now time.Time }

// p7xWallClock returns the real wall clock (UTC); the autonomy ordering
// assertions need runs created after the fixture rows.
type p7xWallClock struct{}

// Now implements the application Clock ports.
func (p7xWallClock) Now() time.Time { return time.Now().UTC() }

// Now implements the application Clock ports.
func (c p7xClock) Now() time.Time { return c.now }

// p7xValidHash returns a valid lowercase sha256-shaped string filled with b.
func p7xValidHash(b byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}

// p7xAuth is the deterministic bearer-token authenticator mounted in front of
// the delivery handlers, mirroring the server's global auth middleware.
type p7xAuth map[string]identity.User

// Authenticate implements identity/interfaces/httpapi.Authenticator.
func (a p7xAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

// p7xUserTokens is the two-user token set used by the delivery HTTP test.
var p7xUserTokens = p7xAuth{
	"p7x-user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	"p7x-admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
}

// p7xBatchReader adapts the real execution batch repository to the delivery
// HTTP surface's BatchReader port. It preserves the repository's
// executionapp.ErrBatchNotFound sentinel, which the handler maps to 404.
type p7xBatchReader struct {
	repo executionapp.BatchRepository
}

// Get implements delivery/interfaces/httpapi.BatchReader.
func (r p7xBatchReader) Get(ctx context.Context, versionID, batchID int64) (*executiondomain.RunBatch, error) {
	batch, err := r.repo.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

// p7xReportGraph is the report service's requirement-graph port. The Task 26
// delivery HTTP scenario uses an empty version graph; the requirement-to-case
// projection is covered by the mindmap unit tests.
type p7xReportGraph struct {
	requirements []deliveryapp.ReportRequirementSource
}

// ListReportRequirements implements delivery/application.ReportCaseGraphReader.
func (g p7xReportGraph) ListReportRequirements(context.Context, int64) ([]deliveryapp.ReportRequirementSource, error) {
	if g.requirements == nil {
		return []deliveryapp.ReportRequirementSource{}, nil
	}
	return g.requirements, nil
}

// p7xAnalysisAgent is the deterministic P1 agent runtime double: it hands out
// ids and never talks to an LLM, so the manual analysis HTTP path can persist a
// running run without a real model. Completion is driven directly on the stored
// run by the autonomy test.
type p7xAnalysisAgent struct{ ids *p7xIDs }

// CreateSession implements delivery/application.AnalysisAgentRuntime.
func (a p7xAnalysisAgent) CreateSession(context.Context, string, string, int64, string, string) (int64, error) {
	return a.ids.Next(), nil
}

// Start implements delivery/application.AnalysisAgentRuntime.
func (a p7xAnalysisAgent) Start(context.Context, int64, string, int64, deliveryapp.AnalysisAgentRunScope) (int64, error) {
	return a.ids.Next(), nil
}

// GetSession implements delivery/application.AnalysisAgentRuntime. It is not
// used by the start path; a completed session is driven through the repository.
func (p7xAnalysisAgent) GetSession(context.Context, int64) (deliveryapp.AnalysisAgentSessionStatus, error) {
	return deliveryapp.AnalysisAgentSessionStatus{}, errors.New("p7x: agent session polling is not used")
}

// Abort implements delivery/application.AnalysisAgentRuntime.
func (p7xAnalysisAgent) Abort(context.Context, int64, int64) error { return nil }

// p7xCreateVersion inserts one version through the real repository. A non-empty
// phase is written with the repository's own continue-phase CAS.
func p7xCreateVersion(t *testing.T, repo *versionpostgres.VersionRepository, id int64, status, phase string) versiondomain.Version {
	t.Helper()
	version := versiondomain.Version{
		ID: id, Name: "p7x version", Status: status, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	}
	if err := repo.Create(context.Background(), &version); err != nil {
		t.Fatalf("create version %d: %v", id, err)
	}
	if phase != "" {
		if _, err := repo.SetContinuePhase(context.Background(), id, phase, 1); err != nil {
			t.Fatalf("set continue phase %q on version %d: %v", phase, id, err)
		}
		version.ContinuePhase = phase
	}
	return version
}

// p7xRequest performs one HTTP request against the delivery handler chain. A
// non-empty token is sent as the bearer credential and a non-empty body as
// application/json.
func p7xRequest(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// p7xRequireStatus asserts the HTTP status code.
func p7xRequireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status=%d want %d body=%s", rec.Code, want, rec.Body)
	}
}

// p7xData decodes the single-resource {data:...} envelope.
func p7xData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode data envelope: %v body=%s", err, rec.Body)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data object: %s", rec.Body)
	}
	return envelope.Data
}

// p7xPage decodes a cursor page envelope.
func p7xPage(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page envelope: %v body=%s", err, rec.Body)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// p7xErrorReason decodes the shared error envelope and returns details.reason.
func p7xErrorReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, rec.Body)
	}
	reason, _ := envelope.Error.Details["reason"].(string)
	return reason
}

// p7xString reads a required decimal-string field and returns it as an int64.
func p7xString(t *testing.T, data map[string]any, key string) int64 {
	t.Helper()
	value, ok := data[key]
	if !ok {
		t.Fatalf("field %q is missing from %v", key, data)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("field %q is %T, want a decimal string: %v", key, value, value)
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		t.Fatalf("field %q=%q is not a decimal id: %v", key, text, err)
	}
	return id
}

// p7xStringField reads an optional string field, defaulting to "".
func p7xStringField(data map[string]any, key string) string {
	text, _ := data[key].(string)
	return text
}

// p7xIntField reads an optional numeric field, defaulting to 0.
func p7xIntField(data map[string]any, key string) int {
	number, _ := data[key].(float64)
	return int(number)
}

// p7xSeedRunBatch inserts one run_batch through the real batch repository and
// returns the stored row.
func p7xSeedRunBatch(t *testing.T, repo executionapp.BatchRepository, batch executiondomain.RunBatch, runs []executiondomain.CaseRun) executiondomain.RunBatch {
	t.Helper()
	if batch.PauseMode == "" {
		batch.PauseMode = executiondomain.PauseModeGraceful
	}
	created, err := repo.CreateBatchWithRuns(context.Background(), batch, runs)
	if err != nil {
		t.Fatalf("create run batch %d: %v", batch.ID, err)
	}
	return created
}

// p7xSeedFailedApplicationRun inserts a failed(application) case run row through
// a direct insert so the delivery analysis/report adapters can read it. The
// snapshot node id is a decimal string as required by the case snapshot schema
// (D29).
func p7xSeedFailedApplicationRun(t *testing.T, pool *pgxpool.Pool, caseRunID, batchID, versionID, caseNodeID int64, title string) {
	t.Helper()
	snapshot := `{"node":{"id":"` + strconv.FormatInt(caseNodeID, 10) + `","title":"` + title +
		`","priority":"P0"},"steps":[],"assertions":[],"sources":[]}`
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot,
			 attempt, status, failure_class, failure_reason, source)
		VALUES ($1, $2, $3, $4, 1, $5, $6::jsonb, 1, 'failed', 'application', 'p7x failure', 'baseline')`,
		caseRunID, batchID, versionID, caseNodeID, p7xValidHash('b'), snapshot); err != nil {
		t.Fatalf("seed failed application case run %d: %v", caseRunID, err)
	}
}

// p7xSeedCaseRun inserts one case-run snapshot with the given attempt/status
// for the report scenario.
func p7xSeedCaseRun(t *testing.T, pool *pgxpool.Pool, caseRunID, batchID, versionID, caseNodeID int64, attempt int, status string) {
	t.Helper()
	snapshot := `{"node":{"id":"` + strconv.FormatInt(caseNodeID, 10) + `","title":"p7x case ` +
		strconv.FormatInt(caseNodeID, 10) + `","priority":"P0"},"steps":[],"assertions":[],"sources":[]}`
	var class any
	if status != "passed" {
		class = "application"
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot,
			 attempt, status, failure_class, source)
		VALUES ($1, $2, $3, $4, 1, $5, $6::jsonb, $7, $8, $9, 'baseline')`,
		caseRunID, batchID, versionID, caseNodeID, p7xValidHash('b'), snapshot, attempt, status, class); err != nil {
		t.Fatalf("seed case run %d: %v", caseRunID, err)
	}
}

// p7xMarkBatchDone flips a planned/running batch to done through the real
// repository.
func p7xMarkBatchDone(t *testing.T, repo executionapp.BatchRepository, batchID int64, from executiondomain.BatchStatus) {
	t.Helper()
	finished := p7xTestTime.Add(time.Minute)
	if _, err := repo.UpdateBatchStatus(context.Background(), batchID, from,
		executiondomain.BatchStatusDone, &finished); err != nil {
		t.Fatalf("mark batch %d %s->done: %v", batchID, from, err)
	}
}

// p7xAnalysisJSONResult builds a valid failure-analysis result document with one
// candidate for the given failed case run.
func p7xAnalysisJSONResult(t *testing.T, caseRunID, caseNodeID int64) json.RawMessage {
	t.Helper()
	result := deliverydomain.EncodeFailureResult([]deliverydomain.FailureCandidate{{
		CaseRunID: caseRunID, CaseNodeID: caseNodeID,
		Title: "p7x checkout fails", Description: "the checkout request returns 500",
		Severity: deliverydomain.SeverityMajor, Confidence: deliverydomain.ConfidenceHigh,
		FailureClass: deliverydomain.FailureClassApplication,
	}}, nil)
	if len(result) == 0 {
		t.Fatal("encoded failure result is empty")
	}
	return result
}
