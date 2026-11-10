package versionapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type stubAttachmentImageService struct {
	rows      []versionapp.AttachmentImageView
	err       error
	lastID    int64
	listCalls int
	reanalyze int

	versionRows   []versionapp.VersionAttachmentView
	versionErr    error
	versionCalls  int
	lastVersionID int64
}

func (s *stubAttachmentImageService) ListImages(_ context.Context, attachmentID int64) ([]versionapp.AttachmentImageView, error) {
	s.listCalls++
	s.lastID = attachmentID
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

func (s *stubAttachmentImageService) ListVersionAttachments(_ context.Context, versionID int64) ([]versionapp.VersionAttachmentView, error) {
	s.versionCalls++
	s.lastVersionID = versionID
	if s.versionErr != nil {
		return nil, s.versionErr
	}
	return s.versionRows, nil
}

func (s *stubAttachmentImageService) Reanalyze(_ context.Context, attachmentID int64) ([]versionapp.AttachmentImageView, error) {
	s.reanalyze++
	s.lastID = attachmentID
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

var _ versionapi.AttachmentImageService = (*stubAttachmentImageService)(nil)

func newAttachmentImageServer(t *testing.T) (http.Handler, *stubAttachmentImageService) {
	t.Helper()
	stub := &stubAttachmentImageService{rows: []versionapp.AttachmentImageView{sampleAttachmentImageView()}}
	handler := versionapi.NewAttachmentHandler(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/attachments/{id}/images", handler.ListImages)
	mux.HandleFunc("POST /api/attachments/{id}/reanalyze", handler.Reanalyze)
	return mux, stub
}

func sampleAttachmentImageView() versionapp.AttachmentImageView {
	return versionapp.AttachmentImageView{
		ID: 9101, AttachmentID: 8001, Seq: 1, ContentRefID: 7101,
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MIME:   "image/png", Width: 800, Height: 600,
		SourceKind: version.AttachmentImageSourceEmbedded,
		Status:     version.AttachmentImageDone,
		Caption:    "登录界面截图：手机号与验证码输入框",
		CreatedAt:  testTime, UpdatedAt: testTime,
	}
}

func TestAttachmentImagesHandler(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	recorder := call(t, mux, http.MethodGet, "/api/attachments/8001/images", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.listCalls != 1 || stub.lastID != 8001 {
		t.Fatalf("service calls = %d last id = %d", stub.listCalls, stub.lastID)
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != "9101" || item["attachment_id"] != "8001" || item["status"] != version.AttachmentImageDone ||
		item["caption"] != "登录界面截图：手机号与验证码输入框" || item["source_kind"] != "embedded" ||
		item["mime"] != "image/png" || item["width"] != float64(800) || item["height"] != float64(600) {
		t.Fatalf("item = %#v", item)
	}
	if item["error"] != "" || item["url"] != "/api/content/"+versionappTestSHA {
		t.Fatalf("item error/url = %#v", item)
	}
	t.Logf("GET /api/attachments/8001/images -> %d %s", recorder.Code, recorder.Body.String())
}

func TestAttachmentImagesHandlerNotFound(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	stub.rows = nil
	stub.err = platform.NotFound("attachment")
	recorder := call(t, mux, http.MethodGet, "/api/attachments/9999/images", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAttachmentImagesHandlerRequiresAuth(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	recorder := call(t, mux, http.MethodGet, "/api/attachments/8001/images", "", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.listCalls != 0 {
		t.Fatal("unauthenticated request reached the service")
	}
}

func TestAttachmentReanalyzeHandler(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	recorder := call(t, mux, http.MethodPost, "/api/attachments/8001/reanalyze", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.reanalyze != 1 || stub.lastID != 8001 {
		t.Fatalf("service calls = %d last id = %d", stub.reanalyze, stub.lastID)
	}
	if items := dataList(t, recorder); len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
}

func TestAttachmentReanalyzeHandlerNotFound(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	stub.rows = nil
	stub.err = platform.NotFound("attachment")
	recorder := call(t, mux, http.MethodPost, "/api/attachments/9999/reanalyze", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.reanalyze != 1 {
		t.Fatalf("service calls = %d, want the handler to surface the service error", stub.reanalyze)
	}
}

func TestAttachmentReanalyzeHandlerRequiresAuth(t *testing.T) {
	mux, stub := newAttachmentImageServer(t)
	recorder := call(t, mux, http.MethodPost, "/api/attachments/8001/reanalyze", "", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.reanalyze != 0 {
		t.Fatal("unauthenticated reanalyze reached the service")
	}
}

func TestAttachmentRoutesValidateID(t *testing.T) {
	mux, _ := newAttachmentImageServer(t)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/attachments/not-a-number/images"},
		{http.MethodPost, "/api/attachments/0/reanalyze"},
	} {
		recorder := call(t, mux, tc.method, tc.path, "", &platform.Actor{ID: 42})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d, body = %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

const versionappTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// ---------- version attachment list (C-05) ----------

func sampleVersionAttachmentView() versionapp.VersionAttachmentView {
	return versionapp.VersionAttachmentView{
		ID:               55,
		Name:             "spec.pdf",
		MIME:             "application/pdf",
		Size:             2048,
		ExtractionStatus: version.AttachmentFailed,
		ErrorCode:        version.AttachmentErrorExtraction,
		ErrorText:        "parser crashed",
		ImageCount:       2,
	}
}

func newVersionAttachmentServer(t *testing.T) (http.Handler, *stubAttachmentImageService) {
	t.Helper()
	stub := &stubAttachmentImageService{
		versionRows: []versionapp.VersionAttachmentView{sampleVersionAttachmentView()},
	}
	handler := versionapi.NewAttachmentHandler(stub)
	mux := http.NewServeMux()
	// Route registration itself belongs to the composition root (Task 20); the
	// local mux pins the handler contract.
	mux.HandleFunc("GET /api/versions/{id}/attachments", handler.ListVersionAttachments)
	return mux, stub
}

func TestVersionAttachmentsHandler(t *testing.T) {
	mux, stub := newVersionAttachmentServer(t)
	recorder := call(t, mux, http.MethodGet, "/api/versions/7/attachments", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.versionCalls != 1 || stub.lastVersionID != 7 {
		t.Fatalf("service calls = %d last version = %d", stub.versionCalls, stub.lastVersionID)
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != "55" || item["name"] != "spec.pdf" || item["mime"] != "application/pdf" ||
		item["size"] != float64(2048) || item["extraction_status"] != version.AttachmentFailed ||
		item["error_code"] != version.AttachmentErrorExtraction || item["error_text"] != "parser crashed" ||
		item["image_count"] != float64(2) {
		t.Fatalf("item = %#v", item)
	}
	t.Logf("GET /api/versions/7/attachments -> %d %s", recorder.Code, recorder.Body.String())
}

func TestVersionAttachmentsHandlerEmptyIsArray(t *testing.T) {
	mux, stub := newVersionAttachmentServer(t)
	stub.versionRows = nil
	recorder := call(t, mux, http.MethodGet, "/api/versions/7/attachments", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 0 {
		t.Fatalf("items = %#v, want an empty array", items)
	}
}

func TestVersionAttachmentsHandlerRequiresAuth(t *testing.T) {
	mux, stub := newVersionAttachmentServer(t)
	recorder := call(t, mux, http.MethodGet, "/api/versions/7/attachments", "", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.versionCalls != 0 {
		t.Fatal("unauthenticated request reached the service")
	}
}

func TestVersionAttachmentsHandlerValidatesID(t *testing.T) {
	mux, stub := newVersionAttachmentServer(t)
	for _, path := range []string{
		"/api/versions/not-a-number/attachments",
		"/api/versions/0/attachments",
	} {
		recorder := call(t, mux, http.MethodGet, path, "", &platform.Actor{ID: 42})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if stub.versionCalls != 0 {
		t.Fatalf("invalid ID reached the service %d times", stub.versionCalls)
	}
}

func TestVersionAttachmentsHandlerUnknownVersion(t *testing.T) {
	mux, stub := newVersionAttachmentServer(t)
	stub.versionRows = nil
	stub.versionErr = platform.NotFound("version")
	recorder := call(t, mux, http.MethodGet, "/api/versions/9999/attachments", "", &platform.Actor{ID: 42})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.versionCalls != 1 {
		t.Fatalf("service calls = %d", stub.versionCalls)
	}
}
