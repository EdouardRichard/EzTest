//go:build integration

package storageapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	storageapi "github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

type staticSettings struct {
	root string
}

func (s staticSettings) String(context.Context, string) (string, error) {
	return s.root, nil
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func uploadBody(t *testing.T, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func newContentServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	pool := testutil.NewDB(t)
	repo := storagepg.New(pool)
	files := storagepg.NewFileStore()
	root := t.TempDir()
	ids, err := id.NewSnowflake(11)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	svc := storageapp.New(repo, files, staticSettings{root: root}, ids, nil, nil)
	handler := storageapi.New(svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := platform.WithActor(r.Context(), platform.Actor{ID: 7, Role: "user"})
		switch {
		case r.URL.Path == "/api/content" && r.Method == http.MethodPost:
			handler.Upload(w, r.WithContext(actor))
		case strings.HasPrefix(r.URL.Path, "/api/content/"):
			r.SetPathValue("sha256", strings.TrimPrefix(r.URL.Path, "/api/content/"))
			handler.Download(w, r.WithContext(actor))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, root
}

func TestContentUploadDownloadAndDedupe(t *testing.T) {
	server, root := newContentServer(t)
	data := []byte("hello over http")
	wantSHA := shaOf(data)

	body, contentType := uploadBody(t, "hello.txt", data)
	resp, err := http.Post(server.URL+"/api/content", contentType, body)
	if err != nil {
		t.Fatalf("POST content: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	var envelope struct {
		Data struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
			MIME   string `json:"mime"`
			URL    string `json:"url"`
		} `json:"data"`
	}
	raw := readAll(t, resp.Body)
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.SHA256 != wantSHA || envelope.Data.Size != int64(len(data)) {
		t.Fatalf("uploaded = %#v", envelope.Data)
	}
	if envelope.Data.URL != "/api/content/"+wantSHA {
		t.Fatalf("url = %q", envelope.Data.URL)
	}
	if strings.Contains(raw, "objects/") || strings.Contains(raw, root) {
		t.Fatalf("response leaks a filesystem path: %s", raw)
	}

	download, err := http.Get(server.URL + "/api/content/" + wantSHA)
	if err != nil {
		t.Fatalf("GET content: %v", err)
	}
	defer download.Body.Close()
	if download.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d", download.StatusCode)
	}
	if got := download.Header.Get("X-Content-SHA256"); got != wantSHA {
		t.Fatalf("X-Content-SHA256 = %q", got)
	}
	if got := download.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if cache := download.Header.Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("cache control = %q", cache)
	}
	got, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("body = %q", got)
	}

	secondBody, secondType := uploadBody(t, "again.txt", data)
	second, err := http.Post(server.URL+"/api/content", secondType, secondBody)
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	defer second.Body.Close()
	var secondEnvelope struct {
		Data struct {
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondEnvelope); err != nil {
		t.Fatalf("decode duplicate: %v", err)
	}
	if second.StatusCode != http.StatusCreated || secondEnvelope.Data.SHA256 != wantSHA {
		t.Fatalf("duplicate = %d %#v", second.StatusCode, secondEnvelope)
	}

	missing, err := http.Get(server.URL + "/api/content/" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.StatusCode)
	}

	bad, err := http.Get(server.URL + "/api/content/not-a-sha")
	if err != nil {
		t.Fatalf("GET invalid: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid sha status = %d", bad.StatusCode)
	}

	// Tampering with the stored bytes must surface as a server error, never as
	// a response that carries a stale X-Content-SHA256.
	onDisk := filepath.Join(root, "objects", wantSHA[0:2], wantSHA[2:4], wantSHA)
	if err := os.WriteFile(onDisk, []byte("corrupted bytes"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	corrupt, err := http.Get(server.URL + "/api/content/" + wantSHA)
	if err != nil {
		t.Fatalf("GET corrupt: %v", err)
	}
	corrupt.Body.Close()
	if corrupt.StatusCode != http.StatusInternalServerError {
		t.Fatalf("corrupt status = %d", corrupt.StatusCode)
	}
}

func TestContentUploadRejectsOversize(t *testing.T) {
	server, _ := newContentServer(t)
	data := make([]byte, storageapp.MaxUploadBytes+1)
	body, contentType := uploadBody(t, "big.bin", data)
	resp, err := http.Post(server.URL+"/api/content", contentType, body)
	if err != nil {
		t.Fatalf("POST oversized: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}
