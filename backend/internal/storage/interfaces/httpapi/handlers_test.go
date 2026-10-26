package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/localfs"
)

// capturingRecorder keeps the audit entries recorded during a test.
type capturingRecorder struct {
	entries []auditdomain.Entry
}

func (r *capturingRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (r *capturingRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return r.entries, false, nil
}

type testAuth struct {
	tokens map[string]identity.User
}

func (a testAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, identity.ErrUnauthenticated
}

func (a testAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a.tokens[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (a testAuth) Logout(context.Context, string) error { return nil }

// withAuditMeta mirrors the server's auditRequestContext for handler tests: it
// stamps the request metadata and the authenticated actor on the context.
func withAuditMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auditapp.WithMeta(r.Context(), auditapp.RequestMeta{
			RequestID: r.Header.Get("X-Request-ID"),
			IP:        "127.0.0.1",
			UserAgent: r.UserAgent(),
		})
		if user, ok := identityhttp.UserFromContext(ctx); ok {
			ctx = auditapp.WithActorID(ctx, user.ID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newStorageTestServer wires the real localfs store and application service
// behind the identity auth/admin middleware.
func newStorageTestServer(t *testing.T) (http.Handler, string, *capturingRecorder) {
	t.Helper()
	root := t.TempDir()
	recorder := &capturingRecorder{}
	service := storageapp.NewService(localfs.New(root), recorder)
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.RequireAdmin(withAuditMeta(next))
	}
	RegisterRoutes(mux, NewHandler(service), guard)
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	h := identityhttp.AuthMiddleware(testAuth{tokens: map[string]identity.User{
		"ezt_admin": admin,
		"ezt_user":  regular,
	}})(mux)
	return h, root, recorder
}

// doStorageRequest performs one request. contentType is set only when non-empty
// so the missing-Content-Type case can be exercised.
func doStorageRequest(t *testing.T, h http.Handler, method, path, token, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("X-Request-ID", "req_storage_test_1234")
	req.Header.Set("User-Agent", "eztest-storage-unit/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// storagePath is the content-addressed path of an uploaded body below root.
func storagePath(root, content string) string {
	sum := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(sum[:])
	return filepath.Join(root, id[:2], id[2:4], id)
}

func sha256Of(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload.Error.Code
}

// TestStorageUploadDownloadAdminOnly is the brief's endpoint test: only admins
// may upload and download, the upload declares a Content-Type, the response
// reports the hash/size/content type, the download streams the bytes back, and
// the create is audited with the authenticated actor.
func TestStorageUploadDownloadAdminOnly(t *testing.T) {
	h, _, recorder := newStorageTestServer(t)
	content := "hello object storage"

	// Authentication and the admin role gate every storage route.
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "anonymous upload", token: "", want: http.StatusUnauthorized},
		{name: "regular upload", token: "ezt_user", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", tc.token, "text/plain",
				strings.NewReader(content))
			if rec.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}

	// Content-Type is required.
	rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "",
		strings.NewReader(content))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing content type=%d want 400 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != platform.CodeInvalidArgument {
		t.Fatalf("missing content type code=%q want %q", code, platform.CodeInvalidArgument)
	}

	// Admin upload returns the content-addressed metadata.
	rec = doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "text/plain",
		strings.NewReader(content))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload=%d want 201 body=%s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			SHA256      string `json:"sha256"`
			Size        int64  `json:"size"`
			ContentType string `json:"content_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode upload %q: %v", rec.Body, err)
	}
	wantSHA := sha256Of(content)
	if created.Data.SHA256 != wantSHA || created.Data.Size != int64(len(content)) || created.Data.ContentType != "text/plain" {
		t.Fatalf("upload payload=%+v want sha=%s size=%d content_type=text/plain",
			created.Data, wantSHA, len(content))
	}

	// The regular user cannot download; the admin receives the exact bytes.
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "anonymous download", token: "", want: http.StatusUnauthorized},
		{name: "regular download", token: "ezt_user", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+wantSHA, tc.token, "", nil)
			if rec.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
	rec = doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+wantSHA, "ezt_admin", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download=%d want 200 body=%s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != content {
		t.Fatalf("downloaded %q want %q", got, content)
	}
	if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(len(content)) {
		t.Fatalf("Content-Length=%q want %d", got, len(content))
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/octet-stream") {
		t.Fatalf("Content-Type=%q", got)
	}

	// Missing and malformed ids are distinguished.
	missing := strings.Repeat("0", 64)
	if rec := doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+missing, "ezt_admin", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing object=%d want 404 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/not-a-sha", "ezt_admin", "", nil).Body.Bytes()); code != platform.CodeInvalidArgument {
		t.Fatalf("malformed id code=%q want %q", code, platform.CodeInvalidArgument)
	}

	// The upload is audited with actor and request metadata, and without any
	// object bytes.
	if len(recorder.entries) != 1 {
		t.Fatalf("audit entries=%d want 1: %+v", len(recorder.entries), recorder.entries)
	}
	entry := recorder.entries[0]
	if entry.Action != auditapp.ActionStorageObjectCreated {
		t.Fatalf("audit action=%q want %q", entry.Action, auditapp.ActionStorageObjectCreated)
	}
	if entry.TargetType != "storage_object" || entry.TargetID != wantSHA {
		t.Fatalf("audit target=%q/%q want storage_object/%s", entry.TargetType, entry.TargetID, wantSHA)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 1 {
		t.Fatalf("audit actor=%v want 1", entry.ActorUserID)
	}
	if entry.RequestID != "req_storage_test_1234" || entry.UserAgent != "eztest-storage-unit/1.0" || entry.IP != "127.0.0.1" {
		t.Fatalf("audit request metadata=%+v", entry)
	}
	if entry.Metadata["sha256"] != wantSHA || entry.Metadata["content_type"] != "text/plain" {
		t.Fatalf("audit metadata=%v", entry.Metadata)
	}
}

// TestStorageUploadDedupesByContent proves repeated uploads reuse the same
// content-addressed file.
func TestStorageUploadDedupesByContent(t *testing.T) {
	h, root, _ := newStorageTestServer(t)
	content := "dedupe me"

	type uploadPayload struct {
		Data struct {
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	var uploads [2]uploadPayload
	for i := range uploads {
		rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "text/plain",
			strings.NewReader(content))
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %d=%d body=%s", i, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &uploads[i]); err != nil {
			t.Fatalf("decode upload %d: %v", i, err)
		}
	}
	if uploads[0].Data.SHA256 != uploads[1].Data.SHA256 {
		t.Fatalf("dedupe sha mismatch: %q vs %q", uploads[0].Data.SHA256, uploads[1].Data.SHA256)
	}
	path := storagePath(root, content)
	var files int
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("stored files=%d want 1", files)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("object missing at %s: %v", path, err)
	}
}

// TestStorageUploadEnforcesLimit proves the 10 MiB HTTP cap: an oversized body
// is rejected without storing or auditing anything, and exactly at the cap is
// accepted.
func TestStorageUploadEnforcesLimit(t *testing.T) {
	h, _, recorder := newStorageTestServer(t)

	oversized := bytes.NewReader(make([]byte, maxObjectBytes+1))
	rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "application/octet-stream", oversized)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload=%d want 413 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != platform.CodeInvalidArgument {
		t.Fatalf("oversized code=%q want %q", code, platform.CodeInvalidArgument)
	}
	if len(recorder.entries) != 0 {
		t.Fatalf("oversized upload was audited: %+v", recorder.entries)
	}

	atLimit := bytes.NewReader(make([]byte, maxObjectBytes))
	rec = doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "application/octet-stream", atLimit)
	if rec.Code != http.StatusCreated {
		t.Fatalf("limit upload=%d want 201 body=%s", rec.Code, rec.Body)
	}
}

// TestStorageDownloadCorruptedObject proves a tampered stored file is reported
// as storage_corrupted instead of being served.
func TestStorageDownloadCorruptedObject(t *testing.T) {
	h, root, _ := newStorageTestServer(t)
	content := "verify on read"

	rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", "ezt_admin", "text/plain",
		strings.NewReader(content))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload=%d body=%s", rec.Code, rec.Body)
	}
	if err := os.WriteFile(storagePath(root, content), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec = doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+sha256Of(content), "ezt_admin", "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupted download=%d want 500 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != platform.CodeStorageCorrupted {
		t.Fatalf("corrupted code=%q want %q", code, platform.CodeStorageCorrupted)
	}
}
