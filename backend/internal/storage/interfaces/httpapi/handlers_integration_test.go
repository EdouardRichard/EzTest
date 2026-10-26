//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/localfs"
	storagepostgres "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// testLogger discards HTTP logs during the integration test.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// login performs POST /api/auth/login and returns the bearer token and the
// authenticated user id from the public payload.
func login(t *testing.T, h http.Handler, username, password string) (string, int64) {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req_storage_login_1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s=%d body=%s", username, rec.Code, rec.Body)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
			User  struct {
				ID int64 `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login %s: %v", username, err)
	}
	if payload.Data.Token == "" || payload.Data.User.ID == 0 {
		t.Fatalf("login %s returned %+v", username, payload.Data)
	}
	return payload.Data.Token, payload.Data.User.ID
}

// doStorageRequest performs one storage API request.
func doStorageRequest(t *testing.T, h http.Handler, method, path, token, contentType string, body io.Reader, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	req.Header.Set("User-Agent", "eztest-storage-integration/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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

// TestStorageEndToEndAgainstDatabase wires the production composition root
// against PostgreSQL: real login/roles, the real local filesystem store, the
// real audit repository and the migration's storage_objects table. It covers
// admin/regular permissions, the upload size limit, a content-addressed
// download and the storage_corrupted path for a tampered file.
func TestStorageEndToEndAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	auditor := auditapp.NewService(auditpostgres.NewRepository(pool))
	root := t.TempDir()
	storageService := storageapp.NewService(
		localfs.New(root),
		storagepostgres.NewMetadataRepository(pool),
		auditor,
	)

	userRepo := identitypostgres.NewUserRepository(pool)
	tokenRepo := identitypostgres.NewTokenRepository(pool)
	hasher := password.NewHasher(12)
	userService := identityapp.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userService.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := userService.Create(ctx, "qa01", "qa-pass-123"); err != nil {
		t.Fatal(err)
	}
	authService := identityapp.NewAuthService(userRepo, tokenRepo, hasher, time.Now, auditor)

	h := server.New(config.Config{Env: "test"}, testLogger(), server.Dependencies{
		Pinger:  pool,
		Auth:    authService,
		Users:   userService,
		Audit:   auditor,
		Storage: storageService,
	})

	// The migration from order.txt is applied by testutil.OpenTestDB.
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'storage_objects'`).Scan(&columns); err != nil {
		t.Fatalf("storage_objects table is missing: %v", err)
	}
	if columns != 5 {
		t.Fatalf("storage_objects columns=%d want 5 (sha256,size,content_type,relative_path,created_at)", columns)
	}

	adminToken, adminID := login(t, h, "admin", "admin-pass-123")
	userToken, _ := login(t, h, "qa01", "qa-pass-123")

	content := "integration content-addressed object"
	wantSHA := sha256.Sum256([]byte(content))
	wantID := hex.EncodeToString(wantSHA[:])

	// A regular user is forbidden; the admin may upload.
	if rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", userToken,
		"text/plain", strings.NewReader(content), "req_storage_forbidden_1"); rec.Code != http.StatusForbidden {
		t.Fatalf("regular upload=%d want 403 body=%s", rec.Code, rec.Body)
	}
	rec := doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", adminToken,
		"text/plain", strings.NewReader(content), "req_storage_create_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin upload=%d want 201 body=%s", rec.Code, rec.Body)
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
	if created.Data.SHA256 != wantID || created.Data.Size != int64(len(content)) || created.Data.ContentType != "text/plain" {
		t.Fatalf("upload payload=%+v", created.Data)
	}
	storedPath := filepath.Join(root, wantID[:2], wantID[2:4], wantID)
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("object not stored at ab/cd/<sha256>: %v", err)
	}

	// The upload persists its durable metadata row in PostgreSQL.
	var (
		storedSHA          string
		storedSize         int64
		storedContentType  string
		storedRelativePath string
		storedCreatedAt    time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT sha256, size, content_type, relative_path, created_at
		FROM storage_objects
		WHERE sha256 = $1`, wantID).
		Scan(&storedSHA, &storedSize, &storedContentType, &storedRelativePath, &storedCreatedAt); err != nil {
		t.Fatalf("storage_objects row missing: %v", err)
	}
	wantRelativePath := wantID[:2] + "/" + wantID[2:4] + "/" + wantID
	if storedSHA != wantID || storedSize != int64(len(content)) ||
		storedContentType != "text/plain" || storedRelativePath != wantRelativePath {
		t.Fatalf("stored metadata=%s/%d/%q/%q want %s/%d/text/plain/%q",
			storedSHA, storedSize, storedContentType, storedRelativePath, wantID, len(content), wantRelativePath)
	}
	if storedCreatedAt.IsZero() {
		t.Fatal("storage_objects.created_at was not set")
	}

	// Re-uploading the same bytes dedupes: the metadata row is reused, the
	// first upload's content type is reported, and no second
	// storage.object_created audit entry is written.
	rec = doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", adminToken,
		"application/octet-stream", strings.NewReader(content), "req_storage_dedupe_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("dedupe upload=%d want 201 body=%s", rec.Code, rec.Body)
	}
	var deduped struct {
		Data struct {
			SHA256      string `json:"sha256"`
			ContentType string `json:"content_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &deduped); err != nil {
		t.Fatalf("decode dedupe upload %q: %v", rec.Body, err)
	}
	if deduped.Data.SHA256 != wantID || deduped.Data.ContentType != "text/plain" {
		t.Fatalf("dedupe payload=%+v want sha=%s content_type=text/plain", deduped.Data, wantID)
	}
	var metadataRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM storage_objects WHERE sha256 = $1`, wantID).Scan(&metadataRows); err != nil {
		t.Fatalf("count metadata rows: %v", err)
	}
	if metadataRows != 1 {
		t.Fatalf("metadata rows after dedupe=%d want 1", metadataRows)
	}

	// The admin downloads the exact bytes with the persisted media type and
	// hardened attachment headers.
	rec = doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+wantID, adminToken, "", nil, "req_storage_get_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin download=%d want 200 body=%s", rec.Code, rec.Body)
	}
	if rec.Body.String() != content {
		t.Fatalf("download=%q want %q", rec.Body.String(), content)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("download Content-Type=%q want persisted text/plain", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("download X-Content-Type-Options=%q want nosniff", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("download Content-Disposition=%q want attachment", got)
	}
	if rec := doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+wantID, userToken, "", nil, "req_storage_get_2"); rec.Code != http.StatusForbidden {
		t.Fatalf("regular download=%d want 403 body=%s", rec.Code, rec.Body)
	}

	// The upload is persisted to the audit log with the authenticated actor and
	// request correlation, and without object bytes.
	entries, _, err := auditor.List(ctx, 100, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	var createdEntries int
	for _, entry := range entries {
		if entry.Action != auditapp.ActionStorageObjectCreated {
			continue
		}
		createdEntries++
		if entry.ActorUserID == nil || *entry.ActorUserID != adminID {
			t.Fatalf("audit actor=%v want admin %d", entry.ActorUserID, adminID)
		}
		if entry.TargetType != "storage_object" || entry.TargetID != wantID {
			t.Fatalf("audit target=%q/%q", entry.TargetType, entry.TargetID)
		}
		if entry.RequestID != "req_storage_create_1" || entry.UserAgent != "eztest-storage-integration/1.0" {
			t.Fatalf("audit request metadata=%+v", entry)
		}
		if entry.Metadata["sha256"] != wantID || entry.Metadata["content_type"] != "text/plain" {
			t.Fatalf("audit metadata=%v", entry.Metadata)
		}
	}
	if createdEntries != 1 {
		t.Fatalf("storage.object_created audit entries=%d want 1 (dedupe must not claim a creation): %+v",
			createdEntries, entries)
	}

	// The HTTP layer caps uploads at 10 MiB.
	oversized := bytes.NewReader(make([]byte, 10<<20+1))
	rec = doStorageRequest(t, h, http.MethodPost, "/api/admin/storage/objects", adminToken,
		"application/octet-stream", oversized, "req_storage_oversized_1")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload=%d want 413 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != platform.CodeInvalidArgument {
		t.Fatalf("oversized code=%q want %q", code, platform.CodeInvalidArgument)
	}

	// Tampering with the stored file turns the download into storage_corrupted.
	if err := os.WriteFile(storedPath, []byte("tampered on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = doStorageRequest(t, h, http.MethodGet, "/api/admin/storage/objects/"+wantID, adminToken, "", nil, "req_storage_get_3")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupted download=%d want 500 body=%s", rec.Code, rec.Body)
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != platform.CodeStorageCorrupted {
		t.Fatalf("corrupted code=%q want %q", code, platform.CodeStorageCorrupted)
	}
}
