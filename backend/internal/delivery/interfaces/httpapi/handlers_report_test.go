package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
)

// reportServer wires the report handlers with fakes and a terminal batch.
func reportServer(t *testing.T, reports *fakeReportService) http.Handler {
	t.Helper()
	return newTestServer(t, HandlerDeps{
		Reports: reports,
		Batches: &fakeBatchReader{batch: terminalBatch(testVersionID, testBatchID)},
	})
}

// reportError builds the T3 export failure error.
func reportError(reason, message string) *domain.Error {
	return &domain.Error{Kind: "internal", Reason: reason, Message: message}
}

// TestReportRoutesRequireAuthentication proves every report route is behind the
// global bearer auth.
func TestReportRoutesRequireAuthentication(t *testing.T) {
	h := reportServer(t, &fakeReportService{})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/reports", ""},
		{http.MethodPost, "/api/versions/101/reports", `{"batchId":"7001"}`},
		{http.MethodGet, "/api/versions/101/reports/9101", ""},
		{http.MethodPost, "/api/versions/101/reports/9101/publish", ""},
		{http.MethodPost, "/api/versions/101/reports/9101/regenerate", ""},
		{http.MethodGet, "/api/versions/101/reports/9101/export?format=markdown", ""},
		{http.MethodGet, "/api/versions/101/reports/9101/diff", ""},
	} {
		rec := doRequest(h, route.method, route.path, "", route.body)
		requireStatus(t, rec, http.StatusUnauthorized)
	}
}

// TestReportListPage proves the list forwards filters and the opaque cursor and
// never returns report content or the filesystem path.
func TestReportListPage(t *testing.T) {
	report := sampleReport()
	report.Content = domain.ReportContent{}
	report.MarkdownPath = ""
	report.MarkdownSHA256 = ""
	report.MarkdownSize = 0
	var gotVersionID int64
	var gotFilter deliveryapp.ReportFilter
	reports := &fakeReportService{listPage: func(_ context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
		gotVersionID, gotFilter = versionID, filter
		return []domain.Report{report}, "next-report-cursor", true, nil
	}}
	h := reportServer(t, reports)

	cursor := encodeCursor(9109)
	rec := doRequest(h, http.MethodGet,
		"/api/versions/101/reports?batchId=7001&status=published&cursor="+cursor+"&limit=5",
		"ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	if gotVersionID != testVersionID || gotFilter.BatchID == nil || *gotFilter.BatchID != 7001 ||
		gotFilter.Status != domain.ReportStatusPublished || gotFilter.Cursor != cursor || gotFilter.Limit != 5 {
		t.Fatalf("version=%d filter=%+v", gotVersionID, gotFilter)
	}
	items, next, hasMore := decodePage(t, rec)
	if len(items) != 1 || next != "next-report-cursor" || !hasMore {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	if got := requireStringField(t, items[0], "id"); got != "9101" {
		t.Fatalf("id=%q", got)
	}
	requireStringField(t, items[0], "versionId")
	requireStringField(t, items[0], "batchId")
	requireNoJSONField(t, items[0], "content")
	requireNoJSONField(t, items[0], "markdownPath")
	markdown, ok := items[0]["markdown"].(map[string]any)
	if !ok {
		t.Fatalf("markdown=%v", items[0]["markdown"])
	}
	if markdown["downloadPath"] != "/api/versions/101/reports/9101/export?format=markdown" {
		t.Fatalf("downloadPath=%v", markdown["downloadPath"])
	}
}

// TestReportListRejectsBadQuery proves limit/cursor/batchId/status validation at
// the HTTP boundary.
func TestReportListRejectsBadQuery(t *testing.T) {
	reports := &fakeReportService{listPage: func(context.Context, int64, deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
		t.Fatal("listPage must not be called for an invalid query")
		return nil, "", false, nil
	}}
	h := reportServer(t, reports)
	for name, path := range map[string]string{
		"limit zero":       "/api/versions/101/reports?limit=0",
		"limit not a num":  "/api/versions/101/reports?limit=x",
		"cursor malformed": "/api/versions/101/reports?cursor=not-a-cursor",
		"batch id invalid": "/api/versions/101/reports?batchId=x",
		"status invalid":   "/api/versions/101/reports?status=archived",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
			requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
		})
	}
}

// TestEnsureReport proves POST reports returns 201 when created, 200 when the
// report already exists, and renders the detail view without file paths.
func TestEnsureReport(t *testing.T) {
	report := sampleReport()
	for _, test := range []struct {
		name       string
		created    bool
		wantStatus int
	}{
		{name: "created", created: true, wantStatus: http.StatusCreated},
		{name: "existing", created: false, wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotVersionID, gotBatchID, gotActor int64
			reports := &fakeReportService{ensure: func(_ context.Context, versionID, batchID, actor int64) (domain.Report, bool, error) {
				gotVersionID, gotBatchID, gotActor = versionID, batchID, actor
				return report, test.created, nil
			}}
			h := reportServer(t, reports)
			rec := doRequest(h, http.MethodPost, "/api/versions/101/reports", "ezt_user", `{"batchId":"7001"}`)
			requireStatus(t, rec, test.wantStatus)
			if gotVersionID != testVersionID || gotBatchID != testBatchID || gotActor != testActorID {
				t.Fatalf("args version=%d batch=%d actor=%d", gotVersionID, gotBatchID, gotActor)
			}
			data := decodeData(t, rec)
			requireStringField(t, data, "id")
			if data["content"] == nil {
				t.Fatalf("detail must carry content: %v", data)
			}
			if data["batchNo"] != float64(2) || data["batchKind"] != "baseline" {
				t.Fatalf("batchNo=%v batchKind=%v", data["batchNo"], data["batchKind"])
			}
			markdown, ok := data["markdown"].(map[string]any)
			if !ok {
				t.Fatalf("markdown=%v", data["markdown"])
			}
			if markdown["sha256"] != strings.Repeat("c", 64) || markdown["sizeBytes"] != float64(128) {
				t.Fatalf("markdown=%v", markdown)
			}
			requireNoJSONField(t, data, "markdownPath")
			if strings.Contains(rec.Body.String(), "9101-r1.md") {
				t.Fatalf("response leaks the stored file path: %s", rec.Body)
			}
		})
	}
}

// TestEnsureReportRejectsBadBody proves a missing or invalid batch id is 422 and
// an unknown batch is 404.
func TestEnsureReportRejectsBadBody(t *testing.T) {
	reports := &fakeReportService{}
	h := reportServer(t, reports)
	for name, body := range map[string]string{
		"missing batch": `{}`,
		"numeric batch": `{"batchId":7001}`,
		"bad batch":     `{"batchId":"x"}`,
		"malformed":     `{"batchId":`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodPost, "/api/versions/101/reports", "ezt_user", body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
		})
	}

	h = newTestServer(t, HandlerDeps{
		Reports: reports,
		Batches: &fakeBatchReader{err: notFoundBatch()},
	})
	rec := doRequest(h, http.MethodPost, "/api/versions/101/reports", "ezt_user", `{"batchId":"7001"}`)
	requireError(t, rec, http.StatusNotFound, "not_found", "batch_not_found", false)

	// A batch of another version is 404 too: the ownership gate stays active.
	h = newTestServer(t, HandlerDeps{
		Reports: reports,
		Batches: &fakeBatchReader{batch: terminalBatch(testVersionID+1, testBatchID)},
	})
	rec = doRequest(h, http.MethodPost, "/api/versions/101/reports", "ezt_user", `{"batchId":"7001"}`)
	requireError(t, rec, http.StatusNotFound, "not_found", "batch_not_found", false)
}

// TestEnsureReportRouteRequiresBatchReader proves POST /reports is only mounted
// with a batch reader: the ownership gate cannot silently disappear when the
// reader is not wired, while list/get/export stays available (spec §7.4).
func TestEnsureReportRouteRequiresBatchReader(t *testing.T) {
	ensureCalled := false
	report := sampleReport()
	reports := &fakeReportService{
		ensure: func(context.Context, int64, int64, int64) (domain.Report, bool, error) {
			ensureCalled = true
			return report, true, nil
		},
		listPage: func(context.Context, int64, deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
			return []domain.Report{report}, "", false, nil
		},
		get: func(context.Context, int64, int64) (domain.Report, error) { return report, nil },
		export: func(context.Context, int64, int64) (domain.ReportExport, error) {
			body := []byte("# report\n")
			return domain.ReportExport{
				ReportID: testReportID, VersionID: testVersionID, BatchID: testBatchID,
				Revision: 1, Content: body, SHA256: sha256Hex(body), Size: int64(len(body)),
				Filename: "report-9101-r1.md",
			}, nil
		},
	}
	// No Batches: the ensure route must not exist.
	h := newTestServer(t, HandlerDeps{Reports: reports})
	rec := doRequest(h, http.MethodPost, "/api/versions/101/reports", "ezt_user", `{"batchId":"7001"}`)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST reports without a batch reader status=%d body=%s want 404/405",
			rec.Code, rec.Body)
	}
	if ensureCalled {
		t.Fatal("ensure must not be reachable without a batch reader")
	}

	for _, path := range []string{
		"/api/versions/101/reports",
		"/api/versions/101/reports/9101",
		"/api/versions/101/reports/9101/export?format=markdown",
	} {
		rec = doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusOK)
	}
}

// TestGetReport proves the detail view is rendered and a missing report is 404.
func TestGetReport(t *testing.T) {
	report := sampleReport()
	reports := &fakeReportService{get: func(context.Context, int64, int64) (domain.Report, error) {
		return report, nil
	}}
	h := reportServer(t, reports)
	rec := doRequest(h, http.MethodGet, "/api/versions/101/reports/9101", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	data := decodeData(t, rec)
	if got := requireStringField(t, data, "id"); got != "9101" {
		t.Fatalf("id=%q", got)
	}
	if data["inputFingerprint"] != strings.Repeat("b", 64) {
		t.Fatalf("inputFingerprint=%v", data["inputFingerprint"])
	}
	if data["status"] != domain.ReportStatusPublished {
		t.Fatalf("status=%v", data["status"])
	}

	missingReports := &fakeReportService{get: func(context.Context, int64, int64) (domain.Report, error) {
		return domain.Report{}, domain.NewNotFound("report", testReportID)
	}}
	rec = doRequest(reportServer(t, missingReports), http.MethodGet, "/api/versions/101/reports/9101", "ezt_user", "")
	requireError(t, rec, http.StatusNotFound, "not_found", "report_not_found", false)
}

// TestPublishReport proves draft→published maps 200 and a non-draft report maps
// 409 report_not_publishable.
func TestPublishReport(t *testing.T) {
	report := sampleReport()
	reports := &fakeReportService{publish: func(_ context.Context, versionID, reportID, actor int64) (domain.Report, error) {
		if versionID != testVersionID || reportID != testReportID || actor != testActorID {
			t.Fatalf("args version=%d report=%d actor=%d", versionID, reportID, actor)
		}
		return report, nil
	}}
	rec := doRequest(reportServer(t, reports), http.MethodPost, "/api/versions/101/reports/9101/publish", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)

	notPublishable := &fakeReportService{publish: func(context.Context, int64, int64, int64) (domain.Report, error) {
		return domain.Report{}, domain.NewStateConflict(domain.ReportReasonNotPublishable, "already published")
	}}
	rec = doRequest(reportServer(t, notPublishable), http.MethodPost, "/api/versions/101/reports/9101/publish", "ezt_user", "")
	requireError(t, rec, http.StatusConflict, "conflict", "report_not_publishable", false)
}

// TestRegenerateReport proves the handler first confirms the addressed revision
// is the latest of its batch: a new revision is 201, an already-existing newer
// revision is returned with 200 without regenerating (spec §7.4).
func TestRegenerateReport(t *testing.T) {
	addressed := sampleReport() // revision 1 of batch 7001
	newRevision := sampleReport()
	newRevision.ID = 9102
	newRevision.Revision = 2

	t.Run("new revision 201", func(t *testing.T) {
		regenerated := false
		reports := &fakeReportService{
			get: func(_ context.Context, versionID, reportID int64) (domain.Report, error) {
				if versionID != testVersionID || reportID != testReportID {
					t.Fatalf("get args version=%d report=%d", versionID, reportID)
				}
				return addressed, nil
			},
			listPage: func(_ context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
				if versionID != testVersionID || filter.BatchID == nil ||
					*filter.BatchID != testBatchID || filter.Limit != 1 {
					t.Fatalf("version=%d filter=%+v want batchId=%d limit=1", versionID, filter, testBatchID)
				}
				return []domain.Report{addressed}, "", false, nil
			},
			regenerate: func(_ context.Context, versionID, reportID, actor int64) (domain.Report, error) {
				if versionID != testVersionID || reportID != testReportID || actor != testActorID {
					t.Fatalf("regenerate args version=%d report=%d actor=%d", versionID, reportID, actor)
				}
				regenerated = true
				return newRevision, nil
			},
		}
		rec := doRequest(reportServer(t, reports), http.MethodPost, "/api/versions/101/reports/9101/regenerate", "ezt_user", "")
		requireStatus(t, rec, http.StatusCreated)
		if !regenerated {
			t.Fatal("regenerate was not called for the latest revision")
		}
		if data := decodeData(t, rec); requireStringField(t, data, "id") != "9102" {
			t.Fatalf("id=%v", data["id"])
		}
	})

	t.Run("newer revision exists 200", func(t *testing.T) {
		// ListPage returns the DB list projection: NULL::jsonb content and no
		// batchNo/kind, so the handler must re-read the newest revision to render
		// the same detail shape as the 201 path.
		listed := newRevision
		listed.Content = domain.ReportContent{}
		listed.MarkdownPath = ""
		listed.MarkdownSHA256 = ""
		listed.MarkdownSize = 0
		regenerated := false
		var gotLatestID int64
		reports := &fakeReportService{
			get: func(_ context.Context, versionID, reportID int64) (domain.Report, error) {
				if versionID != testVersionID {
					t.Fatalf("get version=%d", versionID)
				}
				switch reportID {
				case testReportID:
					return addressed, nil
				case newRevision.ID:
					gotLatestID = reportID
					return newRevision, nil
				default:
					t.Fatalf("get report=%d want %d or %d", reportID, testReportID, newRevision.ID)
					return domain.Report{}, nil
				}
			},
			listPage: func(context.Context, int64, deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
				return []domain.Report{listed}, "", false, nil
			},
			regenerate: func(context.Context, int64, int64, int64) (domain.Report, error) {
				regenerated = true
				return domain.Report{}, errors.New("must not regenerate when a newer revision exists")
			},
		}
		rec := doRequest(reportServer(t, reports), http.MethodPost, "/api/versions/101/reports/9101/regenerate", "ezt_user", "")
		requireStatus(t, rec, http.StatusOK)
		if regenerated {
			t.Fatal("regenerate must not run when a newer revision exists")
		}
		if gotLatestID != newRevision.ID {
			t.Fatalf("get latest revision=%d want the newest id %d", gotLatestID, newRevision.ID)
		}
		data := decodeData(t, rec)
		if data["id"] != "9102" {
			t.Fatalf("id=%v want the latest revision 9102", data["id"])
		}
		if data["batchNo"] != float64(2) || data["batchKind"] != "baseline" {
			t.Fatalf("batchNo=%v batchKind=%v want the hydrated detail", data["batchNo"], data["batchKind"])
		}
		content, ok := data["content"].(map[string]any)
		if !ok || content["schema"] != domain.ReportSchemaV1 {
			t.Fatalf("content=%v want the hydrated content", data["content"])
		}
	})

	t.Run("missing report 404", func(t *testing.T) {
		reports := &fakeReportService{
			get: func(context.Context, int64, int64) (domain.Report, error) {
				return domain.Report{}, domain.NewNotFound("report", testReportID)
			},
			regenerate: func(context.Context, int64, int64, int64) (domain.Report, error) {
				t.Fatal("regenerate must not run for a missing report")
				return domain.Report{}, nil
			},
		}
		rec := doRequest(reportServer(t, reports), http.MethodPost, "/api/versions/101/reports/9101/regenerate", "ezt_user", "")
		requireError(t, rec, http.StatusNotFound, "not_found", "report_not_found", false)
	})
}

// TestExportReport proves the Markdown stream carries X-Content-SHA256 and no
// path, and that another format is rejected.
func TestExportReport(t *testing.T) {
	body := []byte("# report\n")
	digest := sha256Hex(body)
	reports := &fakeReportService{export: func(_ context.Context, versionID, reportID int64) (domain.ReportExport, error) {
		if versionID != testVersionID || reportID != testReportID {
			t.Fatalf("args version=%d report=%d", versionID, reportID)
		}
		return domain.ReportExport{
			ReportID: testReportID, VersionID: testVersionID, BatchID: testBatchID,
			Revision: 1, Content: body, SHA256: digest, Size: int64(len(body)),
			Filename: "report-9101-r1.md",
		}, nil
	}}
	h := reportServer(t, reports)
	for _, path := range []string{
		"/api/versions/101/reports/9101/export?format=markdown",
		"/api/versions/101/reports/9101/export",
	} {
		rec := doRequest(h, http.MethodGet, path, "ezt_user", "")
		requireStatus(t, rec, http.StatusOK)
		if got := rec.Header().Get("X-Content-SHA256"); got != digest {
			t.Fatalf("X-Content-SHA256=%q want %q", got, digest)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
			t.Fatalf("Content-Type=%q", got)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "report-9101-r1.md") {
			t.Fatalf("Content-Disposition=%q", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("body=%q", rec.Body.Bytes())
		}
	}

	rec := doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/export?format=pdf", "ezt_user", "")
	requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
}

// TestExportReportFailureIsRetryable500 proves report_export_failed maps to a
// retryable 500 with the stable reason (spec §7.11).
func TestExportReportFailureIsRetryable500(t *testing.T) {
	reports := &fakeReportService{export: func(context.Context, int64, int64) (domain.ReportExport, error) {
		return domain.ReportExport{}, reportError(domain.ReportReasonExportFailed, "export failed")
	}}
	rec := doRequest(reportServer(t, reports), http.MethodGet,
		"/api/versions/101/reports/9101/export?format=markdown", "ezt_user", "")
	requireError(t, rec, http.StatusInternalServerError, "internal", "report_export_failed", true)
}

// TestDiffReport proves the diff view uses decimal-string ids and forwards the
// optional base report.
func TestDiffReport(t *testing.T) {
	diff := domain.ReportDiff{
		BaseReportID:   9000,
		TargetReportID: testReportID,
		BaseBatchNo:    1,
		TargetBatchNo:  2,
		AddedIssues:    []domain.ReportDiffIssue{{IssueID: 9001, Title: "t", Severity: "major"}},
		FixedIssues:    []domain.ReportDiffIssue{},
		RegressedCases: []domain.ReportDiffCase{{CaseNodeID: 7102, Title: "c", From: "passed", To: "failed"}},
		RecoveredCases: []domain.ReportDiffCase{},
		ChangedCases:   []domain.ReportDiffCase{},
		NewCases:       []domain.ReportDiffNewCase{{CaseNodeID: 7103, Title: "n", Source: "ai_supplement"}},
		RemovedCases:   []domain.ReportDiffNewCase{},
	}
	diff.Summary = domain.ReportDiffSummary{AddedIssues: 1, Regressed: 1, NewCases: 1}
	var gotBase *int64
	reports := &fakeReportService{diff: func(_ context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error) {
		if versionID != testVersionID || reportID != testReportID {
			t.Fatalf("args version=%d report=%d", versionID, reportID)
		}
		gotBase = baseReportID
		return diff, nil
	}}
	h := reportServer(t, reports)

	rec := doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/diff?baseReportId=9000", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if gotBase == nil || *gotBase != 9000 {
		t.Fatalf("baseReportID=%v", gotBase)
	}
	data := decodeData(t, rec)
	if requireStringField(t, data, "baseReportId") != "9000" {
		t.Fatalf("baseReportId=%v", data["baseReportId"])
	}
	if requireStringField(t, data, "targetReportId") != "9101" {
		t.Fatalf("targetReportId=%v", data["targetReportId"])
	}
	added, _ := data["addedIssues"].([]any)
	if len(added) != 1 {
		t.Fatalf("addedIssues=%v", data["addedIssues"])
	}
	if requireStringField(t, added[0].(map[string]any), "issueId") != "9001" {
		t.Fatalf("added issue=%v", added[0])
	}
	regressed, _ := data["regressedCases"].([]any)
	if len(regressed) != 1 {
		t.Fatalf("regressedCases=%v", data["regressedCases"])
	}
	if requireStringField(t, regressed[0].(map[string]any), "caseNodeId") != "7102" {
		t.Fatalf("regressed case=%v", regressed[0])
	}

	// The default base (nil) is forwarded as nil.
	rec = doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/diff", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if gotBase != nil {
		t.Fatalf("default base=%v want nil", gotBase)
	}

	// An invalid base id is a 422.
	rec = doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/diff?baseReportId=x", "ezt_user", "")
	requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)

	// No previous report to diff against maps T3's 404.
	noBase := &fakeReportService{diff: func(context.Context, int64, int64, *int64) (domain.ReportDiff, error) {
		return domain.ReportDiff{}, domain.NewNotFound("report", 0)
	}}
	rec = doRequest(reportServer(t, noBase), http.MethodGet, "/api/versions/101/reports/9101/diff", "ezt_user", "")
	requireError(t, rec, http.StatusNotFound, "not_found", "report_not_found", false)
}

// ---------------------------------------------------------------------------
// Export actor: real application service, fake ports
// ---------------------------------------------------------------------------

type exportVersionReader struct{}

func (exportVersionReader) GetReportVersion(context.Context, int64) (deliveryapp.ReportVersionSource, error) {
	return deliveryapp.ReportVersionSource{ID: testVersionID}, nil
}

type exportExecutionReader struct{}

func (exportExecutionReader) GetReportBatch(context.Context, int64, int64) (deliveryapp.ReportBatchSource, error) {
	return deliveryapp.ReportBatchSource{ID: testBatchID, VersionID: testVersionID, Status: "done"}, nil
}
func (exportExecutionReader) ListReportCaseRuns(context.Context, int64) ([]deliveryapp.ReportCaseRunSource, error) {
	return nil, nil
}
func (exportExecutionReader) PreviousBatchID(context.Context, int64, int64) (int64, bool, error) {
	return 0, false, nil
}

type exportGraphReader struct{}

func (exportGraphReader) ListReportRequirements(context.Context, int64) ([]deliveryapp.ReportRequirementSource, error) {
	return nil, nil
}

type exportIssueReader struct{}

func (exportIssueReader) ListReportIssues(context.Context, int64, int64) ([]deliveryapp.ReportIssueSource, error) {
	return nil, nil
}

type exportReportStore struct{ report domain.Report }

func (s exportReportStore) Create(context.Context, domain.Report) (domain.Report, error) {
	return domain.Report{}, nil
}
func (s exportReportStore) Get(_ context.Context, _, _ int64) (domain.Report, error) {
	return s.report, nil
}
func (s exportReportStore) LatestByBatch(context.Context, int64, int64) (domain.Report, error) {
	return domain.Report{}, domain.ErrReportNotFound
}
func (s exportReportStore) List(context.Context, int64, deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
	return nil, "", false, nil
}
func (s exportReportStore) NextRevision(context.Context, int64, int64) (int, error) { return 2, nil }
func (s exportReportStore) MarkStale(context.Context, int64, int64, string, string, time.Time) (bool, error) {
	return false, nil
}
func (s exportReportStore) MarkPublished(context.Context, int64, int64, time.Time) (bool, error) {
	return false, nil
}
func (s exportReportStore) Delete(context.Context, int64, int64) error { return nil }

type exportFileStore struct{ data []byte }

func (s exportFileStore) Write(context.Context, string, []byte) (string, string, int64, error) {
	return "", "", 0, nil
}
func (s exportFileStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

type exportAudit struct {
	events []deliveryapp.ReportAuditEvent
}

func (a *exportAudit) RecordReportAudit(_ context.Context, event deliveryapp.ReportAuditEvent) error {
	a.events = append(a.events, event)
	return nil
}

type exportClock struct{}

func (exportClock) Now() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }

type exportIDs struct{}

func (exportIDs) Next() int64 { return 999999 }

// TestExportAttachesActorForAudit proves the handler attaches WithReportActor so
// T3's exported audit carries the authenticated user (spec §11.2).
func TestExportAttachesActorForAudit(t *testing.T) {
	content := []byte("# report\n")
	report := sampleReport()
	report.MarkdownSHA256 = sha256Hex(content)
	report.MarkdownSize = int64(len(content))
	audit := &exportAudit{}
	service := deliveryapp.NewReportService(deliveryapp.ReportServiceDeps{
		Versions:  exportVersionReader{},
		Execution: exportExecutionReader{},
		Graph:     exportGraphReader{},
		Issues:    exportIssueReader{},
		Reports:   exportReportStore{report: report},
		Files:     exportFileStore{data: content},
		Audit:     audit,
		IDs:       exportIDs{},
		Clock:     exportClock{},
	})
	mux := http.NewServeMux()
	NewHandlers(HandlerDeps{Reports: service}).RegisterRoutes(mux)
	h := authenticatedForTest(mux)

	rec := doRequest(h, http.MethodGet, "/api/versions/101/reports/9101/export?format=markdown", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if len(audit.events) != 1 {
		t.Fatalf("audit events=%d want 1", len(audit.events))
	}
	event := audit.events[0]
	if event.Action != deliveryapp.ReportActionExported {
		t.Fatalf("action=%q", event.Action)
	}
	if event.ActorID != testActorID {
		t.Fatalf("actor=%d want %d (WithReportActor not attached)", event.ActorID, testActorID)
	}
}

// TestExportRealServiceFailureIsRetryable500 proves T3's real export failure maps
// to 500 retryable true with details.reason report_export_failed.
func TestExportRealServiceFailureIsRetryable500(t *testing.T) {
	report := sampleReport()
	report.MarkdownSHA256 = strings.Repeat("d", 64)
	report.MarkdownSize = 10
	service := deliveryapp.NewReportService(deliveryapp.ReportServiceDeps{
		Versions:  exportVersionReader{},
		Execution: exportExecutionReader{},
		Graph:     exportGraphReader{},
		Issues:    exportIssueReader{},
		Reports:   exportReportStore{report: report},
		Files:     exportFileStore{data: []byte("corrupted")},
		IDs:       exportIDs{},
		Clock:     exportClock{},
	})
	mux := http.NewServeMux()
	NewHandlers(HandlerDeps{Reports: service}).RegisterRoutes(mux)
	rec := doRequest(authenticatedForTest(mux), http.MethodGet,
		"/api/versions/101/reports/9101/export?format=markdown", "ezt_user", "")
	requireError(t, rec, http.StatusInternalServerError, "internal", "report_export_failed", true)
}

// authenticatedForTest applies the identity middleware to a bare mux.
func authenticatedForTest(next http.Handler) http.Handler {
	return identityhttp.AuthMiddleware(testTokens)(next)
}
