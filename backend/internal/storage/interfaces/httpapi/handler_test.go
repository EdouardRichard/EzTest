package storageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

type fakeService struct {
	uploaded     storage.Object
	uploadErr    error
	gotFilename  string
	gotMIME      string
	gotMaxBytes  int64
	gotActorID   int64
	gotData      []byte
	openObject   storage.Object
	openContent  []byte
	openErr      error
	gotSHA       string
	closedReader bool
}

func (f *fakeService) Upload(_ context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error) {
	f.gotFilename, f.gotMIME = filename, mime
	f.gotMaxBytes, f.gotActorID = maxBytes, actorID
	f.gotData, _ = io.ReadAll(r)
	if f.uploadErr != nil {
		return storage.Object{}, f.uploadErr
	}
	return f.uploaded, nil
}

type trackingReadCloser struct {
	*bytes.Reader
	closed *bool
}

func (t trackingReadCloser) Close() error {
	*t.closed = true
	return nil
}

func (f *fakeService) Open(_ context.Context, sha string) (storage.Object, io.ReadCloser, error) {
	f.gotSHA = sha
	if f.openErr != nil {
		return storage.Object{}, nil, f.openErr
	}
	return f.openObject, trackingReadCloser{bytes.NewReader(f.openContent), &f.closedReader}, nil
}

func multipartBody(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func actorRequest(r *http.Request, id int64) *http.Request {
	return r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: id, Role: "user"}))
}

func newHandler() (*Handler, *fakeService) {
	svc := &fakeService{
		uploaded: storage.Object{
			SHA256:       strings.Repeat("ab", 32),
			Size:         11,
			MIME:         "text/plain; charset=utf-8",
			RelativePath: "objects/ab/ab/secret-path",
		},
		openObject: storage.Object{
			SHA256:       strings.Repeat("cd", 32),
			Size:         7,
			MIME:         "application/octet-stream",
			RelativePath: "objects/cd/cd/secret-path",
		},
		openContent: []byte("payload"),
	}
	return New(svc), svc
}

func TestUploadMultipartReturnsObjectWithoutPath(t *testing.T) {
	handler, svc := newHandler()
	body, contentType := multipartBody(t, "file", "hello.txt", []byte("hello world"))
	req := httptest.NewRequest(http.MethodPost, "/api/content", body)
	req.Header.Set("Content-Type", contentType)
	req = actorRequest(req, 42)
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data objectDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.SHA256 != svc.uploaded.SHA256 || envelope.Data.Size != 11 || envelope.Data.MIME != svc.uploaded.MIME {
		t.Fatalf("data = %#v", envelope.Data)
	}
	if envelope.Data.URL != "/api/content/"+svc.uploaded.SHA256 {
		t.Fatalf("url = %q", envelope.Data.URL)
	}
	if strings.Contains(rec.Body.String(), "relative_path") || strings.Contains(rec.Body.String(), "objects/") {
		t.Fatalf("response leaks filesystem path: %s", rec.Body.String())
	}
	if svc.gotMaxBytes != storageapp.MaxUploadBytes {
		t.Fatalf("max bytes = %d", svc.gotMaxBytes)
	}
	if svc.gotActorID != 42 || svc.gotFilename != "hello.txt" {
		t.Fatalf("actor/filename = %d/%q", svc.gotActorID, svc.gotFilename)
	}
	if string(svc.gotData) != "hello world" {
		t.Fatalf("uploaded bytes = %q", svc.gotData)
	}
}

func TestUploadRequiresAuth(t *testing.T) {
	handler, _ := newHandler()
	body, contentType := multipartBody(t, "file", "a.txt", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/api/content", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != "unauthenticated" {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
}

func TestUploadMissingFileIsBadRequest(t *testing.T) {
	handler, _ := newHandler()
	body, contentType := multipartBody(t, "other", "a.txt", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/api/content", body)
	req.Header.Set("Content-Type", contentType)
	req = actorRequest(req, 1)
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUploadServiceErrorIsMapped(t *testing.T) {
	handler, svc := newHandler()
	svc.uploadErr = platform.NewError(http.StatusRequestEntityTooLarge, "payload_too_large", "content exceeds 10 MiB limit")
	body, contentType := multipartBody(t, "file", "a.bin", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/api/content", body)
	req.Header.Set("Content-Type", contentType)
	req = actorRequest(req, 1)
	rec := httptest.NewRecorder()

	handler.Upload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestDownloadStreamsContentWithHeaders(t *testing.T) {
	handler, svc := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/content/"+svc.openObject.SHA256, nil)
	req.SetPathValue("sha256", svc.openObject.SHA256)
	req = actorRequest(req, 7)
	rec := httptest.NewRecorder()

	handler.Download(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-SHA256"); got != svc.openObject.SHA256 {
		t.Fatalf("X-Content-SHA256 = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != svc.openObject.MIME {
		t.Fatalf("content type = %q", got)
	}
	if cache := rec.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") || !strings.Contains(cache, "private") {
		t.Fatalf("cache control = %q", cache)
	}
	if rec.Body.String() != "payload" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "objects/") {
		t.Fatalf("body leaks path")
	}
	if !svc.closedReader {
		t.Fatal("reader was not closed")
	}
	if svc.gotSHA != svc.openObject.SHA256 {
		t.Fatalf("sha = %q", svc.gotSHA)
	}
}

func TestDownloadInvalidSHAIsBadRequest(t *testing.T) {
	handler, _ := newHandler()
	for _, sha := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		req := httptest.NewRequest(http.MethodGet, "/api/content/"+sha, nil)
		req.SetPathValue("sha256", sha)
		req = actorRequest(req, 7)
		rec := httptest.NewRecorder()
		handler.Download(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("sha %q status = %d", sha, rec.Code)
		}
	}
}

func TestDownloadRequiresAuth(t *testing.T) {
	handler, svc := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/content/"+svc.openObject.SHA256, nil)
	req.SetPathValue("sha256", svc.openObject.SHA256)
	rec := httptest.NewRecorder()
	handler.Download(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestDownloadNotFoundAndCorruptErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"not found", platform.NotFound("content"), http.StatusNotFound},
		{"corrupt", storage.ErrCorrupt, http.StatusInternalServerError},
		{"filesystem", errors.New("disk on fire"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, svc := newHandler()
			svc.openErr = tc.err
			req := httptest.NewRequest(http.MethodGet, "/api/content/"+svc.openObject.SHA256, nil)
			req.SetPathValue("sha256", svc.openObject.SHA256)
			req = actorRequest(req, 7)
			rec := httptest.NewRecorder()
			handler.Download(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if rec.Code >= 500 && strings.Contains(rec.Body.String(), "disk on fire") {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
		})
	}
}
