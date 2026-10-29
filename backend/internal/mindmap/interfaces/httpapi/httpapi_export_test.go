package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
)

// exportFake implements mindmaphttp.ExportService.
type exportFake struct {
	createFn   func(context.Context, int64, int64, string) (domain.CaseExport, error)
	listFn     func(context.Context, int64, string, int, string) ([]domain.CaseExport, string, bool, error)
	downloadFn func(context.Context, int64, int64) (io.ReadCloser, domain.CaseExport, error)
}

func (f *exportFake) Create(ctx context.Context, versionID, actor int64, format string) (domain.CaseExport, error) {
	if f.createFn == nil {
		return domain.CaseExport{}, errUnexpected
	}
	return f.createFn(ctx, versionID, actor, format)
}

func (f *exportFake) List(ctx context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error) {
	if f.listFn == nil {
		return nil, "", false, errUnexpected
	}
	return f.listFn(ctx, versionID, format, limit, cursor)
}

func (f *exportFake) Download(ctx context.Context, versionID, exportID int64) (io.ReadCloser, domain.CaseExport, error) {
	if f.downloadFn == nil {
		return nil, domain.CaseExport{}, errUnexpected
	}
	return f.downloadFn(ctx, versionID, exportID)
}

// exportIdxFake implements mindmaphttp.ExportIndex.
type exportIdxFake struct {
	findFn func(context.Context, int64, string, *int64) (domain.CaseExport, error)
}

func (f *exportIdxFake) FindByBaselineFormat(ctx context.Context, versionID int64, format string, baselineID *int64) (domain.CaseExport, error) {
	if f.findFn == nil {
		return domain.CaseExport{}, domain.NewNotFound("export", versionID)
	}
	return f.findFn(ctx, versionID, format, baselineID)
}

// exportFixture is one stored export metadata row.
func exportFixture() domain.CaseExport {
	return domain.CaseExport{
		ID: 980, VersionID: 101, Format: domain.ExportMarkdown,
		FilePath: "versions/101/draft/markdown-980.md", FileSHA256: strings.Repeat("cd", 32),
		SizeBytes: 42, NodeCount: 3,
		CreatedAt: time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC),
	}
}

// TestCreateExportReturns201 proves a fresh export answers 201 with the metadata
// (string id, sha256 and suggested filename) and never exposes the file path.
func TestCreateExportReturns201(t *testing.T) {
	deps := mindmaphttp.Deps{
		Exports: &exportFake{createFn: func(_ context.Context, versionID, actor int64, format string) (domain.CaseExport, error) {
			if versionID != 101 || actor != regularUser.ID || format != domain.ExportMarkdown {
				t.Fatalf("create args=%d/%d/%q", versionID, actor, format)
			}
			return exportFixture(), nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/export", tokenUser, `{"format":"markdown"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /export=%d body=%s, want 201", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "draft/markdown-980.md") {
		t.Fatalf("export body leaks the internal path: %s", rec.Body)
	}
	data := decodeData(t, rec)
	if data["id"] != "980" || data["format"] != "markdown" {
		t.Fatalf("export=%v", data)
	}
	if data["sha256"] != strings.Repeat("cd", 32) {
		t.Fatalf("sha256=%v", data["sha256"])
	}
	if data["fileName"] != "mindmap-markdown-980.md" {
		t.Fatalf("fileName=%v, want the suggested attachment name", data["fileName"])
	}
}

// TestCreateExportReusesBaselineExport proves the service's Reused marker (a
// frozen (version, format, baseline) export served from the reuse lookup) is
// answered with 200 instead of 201. The handler must not pre-check the
// repositories: the service is the authority on reuse.
func TestCreateExportReusesBaselineExport(t *testing.T) {
	deps := mindmaphttp.Deps{
		Exports: &exportFake{createFn: func(context.Context, int64, int64, string) (domain.CaseExport, error) {
			export := exportFixture()
			export.Reused = true
			return export, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/export", tokenUser, `{"format":"markdown"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reused export=%d body=%s, want 200", rec.Code, rec.Body)
	}
}

// TestCreateExportNewDespiteExistingBaselineExport proves the status follows the
// service's Reused flag rather than a handler-side pre-check: a baseline/export
// lookup that would report an existing row must not turn a newly written export
// into 200.
func TestCreateExportNewDespiteExistingBaselineExport(t *testing.T) {
	deps := mindmaphttp.Deps{
		Baselines: &baselineFake{getFn: func(_ context.Context, versionID int64) (domain.CaseBaseline, error) {
			return domain.CaseBaseline{ID: 950, VersionID: versionID}, nil
		}},
		ExportIdx: &exportIdxFake{findFn: func(context.Context, int64, string, *int64) (domain.CaseExport, error) {
			return exportFixture(), nil
		}},
		Exports: &exportFake{createFn: func(context.Context, int64, int64, string) (domain.CaseExport, error) {
			return exportFixture(), nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/export", tokenUser, `{"format":"markdown"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("new export=%d body=%s, want 201", rec.Code, rec.Body)
	}
}

// TestCreateExportRejectsUnsupportedFormat proves the 422 mapping of an
// unsupported server format (PNG is rendered client-side).
func TestCreateExportRejectsUnsupportedFormat(t *testing.T) {
	deps := mindmaphttp.Deps{
		Exports: &exportFake{createFn: func(context.Context, int64, int64, string) (domain.CaseExport, error) {
			return domain.CaseExport{}, domain.NewInvalid("format", "export_format_unsupported", "png is not a server export format")
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/export", tokenUser, `{"format":"png"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported format=%d body=%s, want 422", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "export_format_unsupported" {
		t.Fatalf("reason=%q, want export_format_unsupported", got)
	}
}

// TestDownloadExportSetsAttachmentHeaders pins the download contract of endpoint
// 27: octet-stream attachment, nosniff, RFC 5987 filename and the raw bytes.
func TestDownloadExportSetsAttachmentHeaders(t *testing.T) {
	content := "# mindmap\n"
	deps := mindmaphttp.Deps{
		Exports: &exportFake{downloadFn: func(_ context.Context, versionID, exportID int64) (io.ReadCloser, domain.CaseExport, error) {
			export := exportFixture()
			export.SizeBytes = int64(len(content))
			return io.NopCloser(strings.NewReader(content)), export, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap/exports/980/download", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("download=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; filename*=UTF-8''mindmap-markdown-980.md") {
		t.Fatalf("Content-Disposition=%q", got)
	}
	if rec.Body.String() != content {
		t.Fatalf("body=%q, want the stored bytes", rec.Body)
	}
}

// TestDownloadCorruptedExportIs500 proves a sha256 mismatch surfaces as an
// internal failure that still names export_corrupted in details.reason.
func TestDownloadCorruptedExportIs500(t *testing.T) {
	deps := mindmaphttp.Deps{
		Exports: &exportFake{downloadFn: func(context.Context, int64, int64) (io.ReadCloser, domain.CaseExport, error) {
			return nil, domain.CaseExport{}, &domain.Error{Kind: "internal", Reason: "export_corrupted", Message: "export 980 sha256 mismatch"}
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap/exports/980/download", tokenUser, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupted download=%d body=%s, want 500", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "export_corrupted" {
		t.Fatalf("reason=%q, want export_corrupted", got)
	}
	if strings.Contains(rec.Body.String(), "sha256 mismatch") {
		t.Fatalf("internal error leaked its message: %s", rec.Body)
	}
}

// TestListExportsPage proves the export history page envelope and format filter.
func TestListExportsPage(t *testing.T) {
	deps := mindmaphttp.Deps{
		Exports: &exportFake{listFn: func(_ context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error) {
			if format != "opml" || limit != 10 {
				t.Fatalf("list args=%q/%d", format, limit)
			}
			return []domain.CaseExport{exportFixture()}, "", false, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap/exports?format=opml&limit=10", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /exports=%d body=%s, want 200", rec.Code, rec.Body)
	}
	var page struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0]["id"] != "980" || page.HasMore {
		t.Fatalf("exports page=%+v", page)
	}
}
