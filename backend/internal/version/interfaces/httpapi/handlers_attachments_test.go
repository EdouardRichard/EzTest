package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const bigAttachmentID = int64(9007199254740999)

// seedTextAttachment stores one text attachment fixture with content.
func seedTextAttachment(deps *testDeps, attachment versionapp.Attachment, content []byte) {
	if attachment.VersionID == 0 {
		attachment.VersionID = versionDetailID
	}
	if attachment.RequirementID == 0 {
		attachment.RequirementID = 200
	}
	if attachment.Kind == "" {
		attachment.Kind = domain.AttachmentText
	}
	if attachment.Filename == "" {
		attachment.Filename = "note.txt"
	}
	if attachment.MIMEType == "" {
		attachment.MIMEType = "text/plain"
	}
	if attachment.SizeBytes == 0 {
		attachment.SizeBytes = int64(len(content))
	}
	if attachment.SHA256 == "" {
		attachment.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	if attachment.StoragePath == "" {
		attachment.StoragePath = "100/300_note.txt"
	}
	deps.attachments.seed(attachment, content)
}

// TestAttachmentMultipartUploadDownloadDelete covers the attachment endpoints:
// multipart upload, metadata-only responses, the safe download headers, the
// scoped delete and the 409/422 reason mapping.
func TestAttachmentMultipartUploadDownloadDelete(t *testing.T) {
	t.Run("multipart upload returns metadata only", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.attachments.nextID = bigAttachmentID
		content := []byte("hello attachment")

		recorder := doMultipartRequest(t, handler, "/api/versions/100/requirements/200/attachments", testUserToken,
			"note.txt", "text/plain", content)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "9007199254740999")
		requireStringField(t, data, "kind", "text")
		requireStringField(t, data, "filename", "note.txt")
		requireStringField(t, data, "mimeType", "text/plain")
		requireJSONNumber(t, data, "sizeBytes", int64(len(content)))
		sum := sha256.Sum256(content)
		requireStringField(t, data, "sha256", hex.EncodeToString(sum[:]))
		if _, ok := data["createdAt"].(string); !ok || data["createdAt"] == "" {
			t.Fatalf("createdAt=%#v want an RFC3339 timestamp", data["createdAt"])
		}
		requireMissingField(t, data, "storagePath")
		if strings.Contains(recorder.Body.String(), "storagePath") {
			t.Fatalf("upload response leaks storagePath: %s", recorder.Body.String())
		}

		upload := deps.attachments.lastUpload
		if upload.versionID != versionDetailID || upload.requirementID != 200 {
			t.Fatalf("upload scope=%d/%d want 100/200", upload.versionID, upload.requirementID)
		}
		if upload.filename != "note.txt" || upload.declaredType != "text/plain" {
			t.Fatalf("upload metadata=%q/%q", upload.filename, upload.declaredType)
		}
		if string(upload.data) != string(content) {
			t.Fatalf("upload content=%q want %q", upload.data, content)
		}
		if deps.attachments.lastActor != testUserID {
			t.Fatalf("upload actor=%d want %d", deps.attachments.lastActor, testUserID)
		}
	})

	t.Run("upload validation failures keep the spec reasons", func(t *testing.T) {
		cases := []struct {
			name   string
			err    error
			status int
			code   string
			reason string
		}{
			{"unsupported type", domain.NewInvalid("file", "unsupported_attachment_type", "not allowed"),
				http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "unsupported_attachment_type"},
			{"too large", domain.NewInvalid("file", "attachment_too_large", "too large"),
				http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "attachment_too_large"},
			{"limit exceeded", domain.NewInvalid("file", "attachment_limit_exceeded", "too many"),
				http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "attachment_limit_exceeded"},
			{"invalid content", domain.NewInvalid("file", "invalid_attachment_content", "not utf-8"),
				http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_attachment_content"},
			{"duplicate", domain.NewConflict("duplicate_attachment", "same sha256"),
				http.StatusConflict, platform.CodeConflict, "duplicate_attachment"},
			{"clarified requirement", domain.NewStateConflict("requirement_state_conflict", "read only"),
				http.StatusConflict, platform.CodeConflict, "requirement_state_conflict"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				handler, deps := newTestServer(t)
				deps.attachments.uploadErr = testCase.err
				recorder := doMultipartRequest(t, handler, "/api/versions/100/requirements/200/attachments", testUserToken,
					"note.txt", "text/plain", []byte("content"))
				requireError(t, recorder, testCase.status, testCase.code, testCase.reason)
			})
		}
	})

	t.Run("a missing file field is a 422", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doMultipartWithoutFile(t, handler, "/api/versions/100/requirements/200/attachments", testUserToken)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_attachment_content")
		if apiError.Details["field"] != "file" {
			t.Fatalf("details.field=%v want file", apiError.Details["field"])
		}
	})

	t.Run("list returns metadata and never the storage path", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedTextAttachment(deps, versionapp.Attachment{ID: bigAttachmentID, CreatedBy: testUserID}, []byte("hello"))

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		attachments := decodeArrayData(t, recorder)
		if len(attachments) != 1 {
			t.Fatalf("attachments=%d want 1", len(attachments))
		}
		attachment := attachments[0].(map[string]any)
		requireJSONString(t, attachment, "id", "9007199254740999")
		requireStringField(t, attachment, "filename", "note.txt")
		requireMissingField(t, attachment, "storagePath")
		if strings.Contains(recorder.Body.String(), "storagePath") {
			t.Fatalf("list response leaks storagePath: %s", recorder.Body.String())
		}
	})

	t.Run("download streams the bytes with safe headers", func(t *testing.T) {
		handler, deps := newTestServer(t)
		content := []byte("line one\nline two\n")
		seedTextAttachment(deps, versionapp.Attachment{ID: 300}, content)

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments/300/content", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if got := recorder.Body.String(); got != string(content) {
			t.Fatalf("content=%q want %q", got, content)
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/plain" {
			t.Fatalf("Content-Type=%q want text/plain", got)
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options=%q want nosniff", got)
		}
		if got := recorder.Header().Get("Content-Disposition"); got != "inline; filename*=UTF-8''note.txt" {
			t.Fatalf("Content-Disposition=%q", got)
		}
	})

	t.Run("download encodes a Unicode filename safely", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 300, Filename: "需求说明.md", MIMEType: "text/markdown",
		}, []byte("# 需求"))

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments/300/content", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		disposition := recorder.Header().Get("Content-Disposition")
		const prefix = "inline; filename*=UTF-8''"
		if !strings.HasPrefix(disposition, prefix) {
			t.Fatalf("Content-Disposition=%q want prefix %q", disposition, prefix)
		}
		decoded, err := url.PathUnescape(strings.TrimPrefix(disposition, prefix))
		if err != nil || decoded != "需求说明.md" {
			t.Fatalf("Content-Disposition filename decodes to %q/%v", decoded, err)
		}
		for _, r := range disposition {
			if r > 127 {
				t.Fatalf("Content-Disposition contains a non-ASCII rune: %q", disposition)
			}
		}
	})

	t.Run("download failures map to 404", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.attachments.openErr = domain.NewNotFound("attachment", 300)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments/300/content", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments/not-a-number/content", testUserToken, "")
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
	})

	t.Run("delete removes the metadata and returns 204", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedTextAttachment(deps, versionapp.Attachment{ID: 300}, []byte("hello"))

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200/attachments/300", testUserToken, "")
		requireStatus(t, recorder, http.StatusNoContent)
		if deps.attachments.lastActor != testUserID {
			t.Fatalf("delete actor=%d want %d", deps.attachments.lastActor, testUserID)
		}
		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200/attachments", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if attachments := decodeArrayData(t, recorder); len(attachments) != 0 {
			t.Fatalf("attachments=%v want []", attachments)
		}
	})

	t.Run("upload errors keep the attachment scope from the path", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doMultipartRequest(t, handler, "/api/versions/100/requirements/200/attachments", testUserToken,
			"note.txt", "text/plain", []byte("x"))
		requireStatus(t, recorder, http.StatusCreated)
		if deps.attachments.lastUpload.versionID != 100 || deps.attachments.lastUpload.requirementID != 200 {
			t.Fatalf("upload scope=%d/%d", deps.attachments.lastUpload.versionID, deps.attachments.lastUpload.requirementID)
		}

		recorder = doMultipartRequest(t, handler, "/api/versions/not-a-number/requirements/200/attachments", testUserToken,
			"note.txt", "text/plain", []byte("x"))
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
	})
}
