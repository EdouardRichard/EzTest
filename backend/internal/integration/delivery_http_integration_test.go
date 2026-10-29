//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliveryfilestore "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/filestore"
	deliverypostgres "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	deliveryreport "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/report"
	deliveryversion "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/version"
	deliveryhttp "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executiondelivery "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/delivery"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestP7DeliveryHTTPIntegration drives the real delivery HTTP surface 鈥?the T12
// handlers over the T2/T3/T4 services and the PostgreSQL repositories 鈥?against
// a real database. It covers issue create/list/detail/CAS-confirm/CAS-dismiss,
// report ensure/regenerate/diff/export, the manual failure-analysis run views,
// the bearer-auth chain and version/batch ownership.
//
// The cross-version global issue/report list queries are T17b (wired in the
// composition root by T17c): this fixture mounts them through the same real
// GlobalReaderService so the global centres are exercised over PostgreSQL too.
func TestP7DeliveryHTTPIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	ids := p7xNewIDs(910000)
	clock := p7xClock{now: p7xTestTime}

	versionRepo := versionpostgres.NewVersionRepository(pool)
	versionSvc := versionapp.NewVersionService(versionRepo, nil, nil, nil)
	p7xCreateVersion(t, versionRepo, 5101, versiondomain.VersionExecuting, "")
	p7xCreateVersion(t, versionRepo, 5102, versiondomain.VersionExecuting, "")

	issueRepo := deliverypostgres.NewIssueRepository(pool)
	issueSvc := deliveryapp.NewIssueService(deliveryapp.IssueServiceDeps{
		Store:    issueRepo,
		Versions: deliveryversion.NewIssueVersionReader(versionSvc),
		IDs:      ids,
		Clock:    clock,
	})
	reportRoot := t.TempDir()
	reportFiles, err := deliveryfilestore.New(reportRoot)
	if err != nil {
		t.Fatalf("report filestore: %v", err)
	}
	executionReads := executiondelivery.NewReadStore(pool)
	reportSvc := deliveryapp.NewReportService(deliveryapp.ReportServiceDeps{
		Versions:  deliveryversion.NewReportVersionReader(versionSvc),
		Execution: executiondelivery.NewReportExecution(executionReads, nil, nil),
		Graph:     p7xReportGraph{},
		Issues:    deliveryreport.NewIssueSources(issueRepo, nil),
		Reports:   deliverypostgres.NewReportRepository(pool),
		Files:     reportFiles,
		IDs:       ids,
		Clock:     clock,
	})
	analysisSvc := deliveryapp.NewAnalysisService(deliveryapp.AnalysisDeps{
		Runs:      deliverypostgres.NewAnalysisRepository(pool),
		Execution: executiondelivery.NewAnalysisExecution(executionReads),
		Issues:    issueSvc,
		Agents:    p7xAnalysisAgent{ids: ids},
		IDs:       ids,
		Clock:     clock,
	})

	batchRepo := executionpostgres.NewBatchRepository(pool)
	finished := p7xTestTime.Add(5 * time.Minute)
	batch := executiondomain.RunBatch{
		ID: 51011, VersionID: 5101, BatchNo: 1, Status: executiondomain.BatchStatusDone,
		Kind: executiondomain.BatchKindBaseline, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 1, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
		FinishedAt: &finished,
	}
	p7xSeedRunBatch(t, batchRepo, batch, nil)
	// The report scenario starts from a passed attempt; the analysis scenario
	// later appends a failed attempt for the same case node.
	p7xSeedCaseRun(t, pool, 6101, 51011, 5101, 6001, 1, "passed")
	// A planned batch of the same version proves the analysis terminality gate.
	planned := executiondomain.RunBatch{
		ID: 51012, VersionID: 5101, BatchNo: 2, Status: executiondomain.BatchStatusPlanned,
		Kind: executiondomain.BatchKindIncremental, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 1, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
	p7xSeedRunBatch(t, batchRepo, planned, nil)

	globalStore := deliverypostgres.NewGlobalReadRepository(pool)
	globalReader := deliveryapp.NewGlobalReaderService(deliveryapp.GlobalReaderDeps{
		Issues:   globalStore,
		Reports:  globalStore,
		Versions: deliveryversion.NewVersionNamesReader(versionSvc),
	})
	handlers := deliveryhttp.NewHandlers(deliveryhttp.HandlerDeps{
		Issues:        issueSvc,
		IssueRefs:     issueSvc,
		Reports:       reportSvc,
		Analyses:      analysisSvc,
		Batches:       p7xBatchReader{repo: batchRepo},
		GlobalIssues:  globalReader,
		GlobalReports: globalReader,
	})
	mux := http.NewServeMux()
	handlers.RegisterRoutes(mux)
	handler := identityhttp.AuthMiddleware(p7xUserTokens)(mux)

	t.Run("authentication", func(t *testing.T) {
		rec := p7xRequest(t, handler, http.MethodGet, "/api/versions/5101/issues", "", "")
		p7xRequireStatus(t, rec, http.StatusUnauthorized)
		rec = p7xRequest(t, handler, http.MethodGet, "/api/versions/5101/issues", "not-a-token", "")
		p7xRequireStatus(t, rec, http.StatusUnauthorized)

		// T17b's cross-version centres are mounted when their readers are wired
		// (T17c composition root); this fixture mirrors that wiring.
		rec = p7xRequest(t, handler, http.MethodGet, "/api/issues", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		rec = p7xRequest(t, handler, http.MethodGet, "/api/reports", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)

		// A human issue for a nonexistent version is a 404, never an orphan row.
		rec = p7xRequest(t, handler, http.MethodPost, "/api/versions/5999/issues", "p7x-user",
			`{"title":"ghost","severity":"major"}`)
		p7xRequireStatus(t, rec, http.StatusNotFound)
		if reason := p7xErrorReason(t, rec); reason != "version_not_found" {
			t.Fatalf("missing version reason = %q, want version_not_found", reason)
		}
	})

	var confirmedIssueID int64
	t.Run("issue lifecycle", func(t *testing.T) {
		rec := p7xRequest(t, handler, http.MethodPost, "/api/versions/5101/issues", "p7x-user",
			`{"title":"Checkout fails","severity":"major","category":"defect","description":"the checkout request returns 500","expected":"order created","actual":"500","reproSteps":"open cart and submit"}`)
		p7xRequireStatus(t, rec, http.StatusCreated)
		created := p7xData(t, rec)
		confirmedIssueID = p7xString(t, created, "id")
		if confirmedIssueID <= 0 || p7xStringField(created, "status") != "open" ||
			p7xStringField(created, "source") != "human" || p7xIntField(created, "revision") != 1 ||
			p7xStringField(created, "versionId") != "5101" {
			t.Fatalf("created issue = %v", created)
		}
		if p7xStringField(created, "createdBy") != "2" {
			t.Fatalf("createdBy = %q, want the authenticated actor 2", created["createdBy"])
		}

		// The detail view reads the real refs projection (no refs on a human issue).
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/issues/"+created["id"].(string), "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		detail := p7xData(t, rec)
		if p7xStringField(detail, "title") != "Checkout fails" || p7xStringField(detail, "actual") != "500" {
			t.Fatalf("issue detail = %v", detail)
		}
		if refs, ok := detail["refs"].([]any); !ok || len(refs) != 0 {
			t.Fatalf("issue refs = %v, want an empty array", detail["refs"])
		}

		// Version-scoped list: the issue belongs to 5101 only.
		rec = p7xRequest(t, handler, http.MethodGet, "/api/versions/5101/issues", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		items, _, more := p7xPage(t, rec)
		if len(items) != 1 || p7xString(t, items[0], "id") != confirmedIssueID || more {
			t.Fatalf("version 5101 issues = %v more=%v", items, more)
		}
		rec = p7xRequest(t, handler, http.MethodGet, "/api/versions/5102/issues", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		if items, _, _ := p7xPage(t, rec); len(items) != 0 {
			t.Fatalf("version 5102 issues = %v, want none", items)
		}

		// CAS confirm: expectedRevision 1 wins and the revision is bumped.
		rec = p7xRequest(t, handler, http.MethodPatch,
			"/api/versions/5101/issues/"+created["id"].(string), "p7x-user",
			`{"status":"confirmed","expectedRevision":1,"comment":"verified"}`)
		p7xRequireStatus(t, rec, http.StatusOK)
		confirmed := p7xData(t, rec)
		if p7xStringField(confirmed, "status") != "confirmed" || p7xIntField(confirmed, "revision") != 2 ||
			p7xStringField(confirmed, "confirmedBy") != "2" {
			t.Fatalf("confirmed issue = %v", confirmed)
		}
		var storedStatus string
		var storedRevision int
		if err := pool.QueryRow(ctx,
			`SELECT status, revision FROM issue WHERE id = $1 AND version_id = 5101`,
			confirmedIssueID).Scan(&storedStatus, &storedRevision); err != nil {
			t.Fatalf("read stored issue: %v", err)
		}
		if storedStatus != "confirmed" || storedRevision != 2 {
			t.Fatalf("stored issue = %s/%d, want confirmed/2", storedStatus, storedRevision)
		}

		// A stale revision loses the CAS with the stable conflict reason.
		rec = p7xRequest(t, handler, http.MethodPatch,
			"/api/versions/5101/issues/"+created["id"].(string), "p7x-user",
			`{"status":"confirmed","expectedRevision":1}`)
		p7xRequireStatus(t, rec, http.StatusConflict)
		if reason := p7xErrorReason(t, rec); reason != "issue_state_conflict" {
			t.Fatalf("stale CAS reason = %q, want issue_state_conflict", reason)
		}

		// A cross-version issue id is indistinguishable from a missing one.
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5102/issues/"+created["id"].(string), "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusNotFound)
		if reason := p7xErrorReason(t, rec); reason != "issue_not_found" {
			t.Fatalf("foreign issue reason = %q, want issue_not_found", reason)
		}
	})

	t.Run("issue dismiss", func(t *testing.T) {
		rec := p7xRequest(t, handler, http.MethodPost, "/api/versions/5101/issues", "p7x-user",
			`{"title":"Slow page","severity":"minor"}`)
		p7xRequireStatus(t, rec, http.StatusCreated)
		created := p7xData(t, rec)
		issuePath := "/api/versions/5101/issues/" + created["id"].(string)

		// A dismissal requires a reason.
		rec = p7xRequest(t, handler, http.MethodPatch, issuePath, "p7x-user",
			`{"status":"dismissed","expectedRevision":1,"comment":"  "}`)
		p7xRequireStatus(t, rec, http.StatusUnprocessableEntity)
		if reason := p7xErrorReason(t, rec); reason != "issue_dismiss_reason_required" {
			t.Fatalf("dismiss reason = %q, want issue_dismiss_reason_required", reason)
		}

		rec = p7xRequest(t, handler, http.MethodPatch, issuePath, "p7x-admin",
			`{"status":"dismissed","expectedRevision":1,"comment":"expected product behaviour"}`)
		p7xRequireStatus(t, rec, http.StatusOK)
		dismissed := p7xData(t, rec)
		if p7xStringField(dismissed, "status") != "dismissed" ||
			p7xStringField(dismissed, "dismissedReason") != "expected product behaviour" ||
			p7xStringField(dismissed, "dismissedBy") != "1" {
			t.Fatalf("dismissed issue = %v", dismissed)
		}
	})

	t.Run("report lifecycle", func(t *testing.T) {
		rec := p7xRequest(t, handler, http.MethodPost, "/api/versions/5101/reports", "p7x-user",
			`{"batchId":"51011"}`)
		p7xRequireStatus(t, rec, http.StatusCreated)
		first := p7xData(t, rec)
		firstID := p7xString(t, first, "id")
		if p7xIntField(first, "revision") != 1 || p7xStringField(first, "status") != "published" ||
			p7xStringField(first, "batchId") != "51011" {
			t.Fatalf("first report = %v", first)
		}
		markdown, _ := first["markdown"].(map[string]any)
		sha, _ := markdown["sha256"].(string)
		size := p7xIntField(markdown, "sizeBytes")
		if len(sha) != 64 || size <= 0 {
			t.Fatalf("report markdown metadata = %v", markdown)
		}
		// The Markdown artifact really exists under the configured report root.
		var markdownPath string
		if err := pool.QueryRow(ctx, `SELECT markdown_path FROM report WHERE id = $1`, firstID).Scan(&markdownPath); err != nil {
			t.Fatalf("read stored report: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(reportRoot, markdownPath)); err != nil || int64(len(data)) != int64(size) {
			t.Fatalf("read markdown artifact %q: size=%d err=%v", markdownPath, size, err)
		}

		// Ensure is idempotent on a live revision.
		rec = p7xRequest(t, handler, http.MethodPost, "/api/versions/5101/reports", "p7x-user",
			`{"batchId":"51011"}`)
		p7xRequireStatus(t, rec, http.StatusOK)
		if again := p7xData(t, rec); p7xString(t, again, "id") != firstID {
			t.Fatalf("second ensure id = %v, want %d", again["id"], firstID)
		}
		var reportCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM report WHERE version_id = 5101 AND batch_id = 51011`).Scan(&reportCount); err != nil {
			t.Fatalf("count reports: %v", err)
		}
		if reportCount != 1 {
			t.Fatalf("report count = %d, want 1 after an idempotent ensure", reportCount)
		}

		// A new attempt changes the report fingerprint: the published revision is
		// marked stale on read, then regenerated as revision 2.
		p7xSeedCaseRun(t, pool, 6102, 51011, 5101, 6001, 2, "failed")
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/reports/"+first["id"].(string), "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		stale := p7xData(t, rec)
		if p7xStringField(stale, "status") != "stale" || p7xStringField(stale, "staleReason") == "" {
			t.Fatalf("report after input change = %v, want stale with a reason", stale)
		}
		rec = p7xRequest(t, handler, http.MethodPost,
			"/api/versions/5101/reports/"+first["id"].(string)+"/regenerate", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusCreated)
		second := p7xData(t, rec)
		secondID := p7xString(t, second, "id")
		if secondID == firstID || p7xIntField(second, "revision") != 2 ||
			p7xStringField(second, "status") != "published" {
			t.Fatalf("second report = %v", second)
		}

		// Diff revision 2 against revision 1: the passed case regressed.
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/reports/"+second["id"].(string)+"/diff?baseReportId="+first["id"].(string),
			"p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		diff := p7xData(t, rec)
		if p7xString(t, diff, "baseReportId") != firstID || p7xString(t, diff, "targetReportId") != secondID {
			t.Fatalf("diff ids = %v", diff)
		}
		summary, _ := diff["summary"].(map[string]any)
		if p7xIntField(summary, "regressed") != 1 {
			t.Fatalf("diff summary = %v, want one regressed case", summary)
		}

		// Export streams the verified Markdown with its SHA256 header.
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/reports/"+second["id"].(string)+"/export?format=markdown", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		secondMarkdown, _ := second["markdown"].(map[string]any)
		secondSHA, _ := secondMarkdown["sha256"].(string)
		if got := rec.Header().Get("X-Content-SHA256"); got != secondSHA {
			t.Fatalf("export sha256 = %q, want %q", got, secondSHA)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/markdown") ||
			!strings.Contains(rec.Body.String(), "#") {
			t.Fatalf("export headers/body = %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
		}

		// Ownership: a foreign version cannot read the report, and an ensure for
		// a batch outside the addressed version is a 404 batch_not_found.
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5102/reports/"+first["id"].(string), "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusNotFound)
		if reason := p7xErrorReason(t, rec); reason != "report_not_found" {
			t.Fatalf("foreign report reason = %q, want report_not_found", reason)
		}
		rec = p7xRequest(t, handler, http.MethodPost, "/api/versions/5102/reports", "p7x-user",
			`{"batchId":"51011"}`)
		p7xRequireStatus(t, rec, http.StatusNotFound)
		if reason := p7xErrorReason(t, rec); reason != "batch_not_found" {
			t.Fatalf("foreign batch reason = %q, want batch_not_found", reason)
		}
	})

	t.Run("analysis run views", func(t *testing.T) {
		// The failed attempt seeded by the report scenario is a real
		// failed(application) case, so the manual failure analysis is applicable.
		rec := p7xRequest(t, handler, http.MethodPost,
			"/api/versions/5101/batches/51011/analysis", "p7x-user", `{"kind":"failure"}`)
		p7xRequireStatus(t, rec, http.StatusAccepted)
		run := p7xData(t, rec)
		runID := p7xString(t, run, "id")
		if runID <= 0 || p7xStringField(run, "kind") != "failure" ||
			p7xStringField(run, "status") != "running" ||
			p7xStringField(run, "versionId") != "5101" || p7xStringField(run, "batchId") != "51011" {
			t.Fatalf("started run = %v", run)
		}

		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/batches/51011/analysis", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		var listed struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode analysis list: %v body=%s", err, rec.Body)
		}
		if len(listed.Data) != 1 || p7xString(t, listed.Data[0], "id") != runID {
			t.Fatalf("analysis list = %v, want run %d", listed.Data, runID)
		}
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5101/batches/51011/analysis/"+run["id"].(string), "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusOK)
		view := p7xData(t, rec)
		if p7xString(t, view, "id") != runID || p7xStringField(view, "createdAt") == "" {
			t.Fatalf("analysis detail = %v", view)
		}
		// A second start while the run is active is a 409 analysis_active.
		rec = p7xRequest(t, handler, http.MethodPost,
			"/api/versions/5101/batches/51011/analysis", "p7x-user", `{"kind":"failure"}`)
		p7xRequireStatus(t, rec, http.StatusConflict)
		if reason := p7xErrorReason(t, rec); reason != "analysis_active" {
			t.Fatalf("second start reason = %q, want analysis_active", reason)
		}

		// The terminality gate rejects the planned batch before the service runs.
		rec = p7xRequest(t, handler, http.MethodPost,
			"/api/versions/5101/batches/51012/analysis", "p7x-user", `{"kind":"failure"}`)
		p7xRequireStatus(t, rec, http.StatusConflict)
		if reason := p7xErrorReason(t, rec); reason != "analysis_not_applicable" {
			t.Fatalf("planned batch reason = %q, want analysis_not_applicable", reason)
		}
		// A foreign version/batch pair is a 404 batch_not_found.
		rec = p7xRequest(t, handler, http.MethodGet,
			"/api/versions/5102/batches/51011/analysis", "p7x-user", "")
		p7xRequireStatus(t, rec, http.StatusNotFound)
		if reason := p7xErrorReason(t, rec); reason != "batch_not_found" {
			t.Fatalf("foreign batch reason = %q, want batch_not_found", reason)
		}
	})
}
