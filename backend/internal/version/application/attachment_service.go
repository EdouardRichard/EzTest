package application

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Attachment is an attachment aggregate.
type Attachment = domain.Attachment

// attachmentSniffBytes is the prefix http.DetectContentType needs.
const attachmentSniffBytes = 512

// maxAttachmentFilenameBytes keeps "<attachmentID>_<filename>" inside the
// 255-byte / 255-UTF-16-unit component limit of common filesystems even when
// the filename is made of 4-byte runes: the 120-rune cap alone can be 480
// bytes, which the operating system would reject at rename time.
const maxAttachmentFilenameBytes = 200

// Limits bounds one attachment upload and the attachment-derived reads. The
// zero value is valid: every zero or negative field falls back to the spec §4.1
// constant, so the composition root can pass Limits{} or DefaultLimits().
type Limits struct {
	// MaxBytesPerFile bounds one uploaded file (default 10 MiB).
	MaxBytesPerFile int64
	// MaxPerRequirement bounds the attachments of one requirement (default 10).
	MaxPerRequirement int
	// MaxPerVersion bounds the attachments of one version (default 50).
	MaxPerVersion int
	// MaxTextBytes bounds one attachment.read_text result (default 64 KiB).
	MaxTextBytes int
	// MaxImageBytes bounds one image injected into an agent run (default 2 MiB).
	MaxImageBytes int64
}

// DefaultLimits returns the P3 limits of spec §4.1. It is the normal value for
// the composition root; Limits{} behaves the same through normalization.
func DefaultLimits() Limits {
	return Limits{
		MaxBytesPerFile:   domain.MaxAttachmentBytes,
		MaxPerRequirement: domain.MaxAttachmentsPerRequirement,
		MaxPerVersion:     domain.MaxAttachmentsPerVersion,
		MaxTextBytes:      domain.MaxInjectedTextBytesPerFile,
		MaxImageBytes:     domain.MaxAgentImageBytes,
	}
}

// normalized fills zero or negative fields with the spec defaults. It also
// guards against an operator lowering a limit below one.
func (l Limits) normalized() Limits {
	defaults := DefaultLimits()
	if l.MaxBytesPerFile <= 0 {
		l.MaxBytesPerFile = defaults.MaxBytesPerFile
	}
	if l.MaxPerRequirement <= 0 {
		l.MaxPerRequirement = defaults.MaxPerRequirement
	}
	if l.MaxPerVersion <= 0 {
		l.MaxPerVersion = defaults.MaxPerVersion
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = defaults.MaxTextBytes
	}
	if l.MaxImageBytes <= 0 {
		l.MaxImageBytes = defaults.MaxImageBytes
	}
	return l
}

// attachmentExtensionMIMEs maps each allowed filename extension (lower-cased)
// to the MIME types a client may declare for it. The extension itself decides
// the stored canonical MIME type; this list enforces the "extension must match"
// half of the spec §6.7 double check while accepting the text/plain alias
// browsers commonly report for text documents and the two YAML aliases.
var attachmentExtensionMIMEs = map[string][]string{
	".txt":  {"text/plain"},
	".md":   {"text/markdown", "text/plain"},
	".json": {"application/json", "text/plain"},
	".yaml": {"application/yaml", "text/yaml", "text/plain"},
	".yml":  {"application/yaml", "text/yaml", "text/plain"},
	".png":  {"image/png"},
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
}

// attachmentCanonicalMIME maps an allowed extension to the MIME type persisted
// in requirement_attachments.mime_type (which is also the download
// Content-Type). The value is always one of the frozen domain allow-list.
var attachmentCanonicalMIME = map[string]string{
	".txt":  "text/plain",
	".md":   "text/markdown",
	".json": "application/json",
	".yaml": "application/yaml",
	".yml":  "text/yaml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
}

// AttachmentService implements the requirement attachment use cases: upload to
// the filesystem store, bounded text reads, transient image data URLs for the
// agent run, and metadata-first deletes (spec §6.7). It depends on the
// repository for metadata, the AttachmentStore port for bytes, the event
// publisher for post-commit events, and the P3 id generator.
type AttachmentService struct {
	repo   RequirementRepository
	store  AttachmentStore
	limits Limits
	events EventPublisher
	ids    IDGenerator
}

// AttachmentServiceOption customises the attachment service dependencies.
type AttachmentServiceOption func(*AttachmentService)

// WithAttachmentIDGenerator injects the Snowflake id generator. The option is
// deliberately named per service so the shared application options cannot be
// confused; without it the package-wide DefaultIDGenerator is used.
func WithAttachmentIDGenerator(ids IDGenerator) AttachmentServiceOption {
	return func(s *AttachmentService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// NewAttachmentService wires the attachment use cases to their repository,
// filesystem store, limits and event publisher. An empty Limits value uses the
// spec §4.1 defaults; T16 injects the shared Snowflake generator through
// WithAttachmentIDGenerator.
func NewAttachmentService(repo RequirementRepository, store AttachmentStore, limits Limits, events EventPublisher, opts ...AttachmentServiceOption) *AttachmentService {
	service := &AttachmentService{
		repo:   repo,
		store:  store,
		limits: limits.normalized(),
		events: events,
		ids:    DefaultIDGenerator(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Upload validates, streams and persists one attachment on a draft requirement.
// The file is written before the metadata row (spec §6.10) and removed again if
// a later validation, the duplicate-sha check or the insert fails. A successful
// upload publishes attachment.added after the commit.
func (s *AttachmentService) Upload(ctx context.Context, versionID, reqID int64, filename, declaredType string, r io.Reader, actor int64) (*Attachment, error) {
	if s.repo == nil || s.store == nil {
		return nil, errors.New("version: attachment service is not configured")
	}
	if r == nil {
		return nil, domain.NewInvalid("file", "invalid_attachment_content", "attachment content is required")
	}
	requirement, err := s.loadRequirement(ctx, versionID, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.Status != domain.RequirementDraft {
		return nil, requirementStateConflictError(reqID, requirement.Status)
	}

	safeName, err := sanitizeAttachmentFilename(filename)
	if err != nil {
		return nil, err
	}
	mimeType, kind, err := attachmentTypeForFilename(safeName)
	if err != nil {
		return nil, err
	}
	if err := validateDeclaredAttachmentType(declaredType, safeName, kind); err != nil {
		return nil, err
	}
	if err := s.checkAttachmentCounts(ctx, reqID, versionID); err != nil {
		return nil, err
	}

	id := s.ids.Next()
	if id <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}

	// Sniff a bounded prefix for the content/extension double check, then
	// replay it to the streaming store write.
	head := make([]byte, attachmentSniffBytes)
	n, readErr := io.ReadFull(r, head)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("version: read attachment head: %w", readErr)
	}
	head = head[:n]
	if err := validateDetectedAttachmentMIME(detectAttachmentMIME(head), mimeType, kind); err != nil {
		return nil, err
	}

	saved, err := s.store.Save(ctx, versionID, id, safeName, io.MultiReader(bytes.NewReader(head), r))
	if err != nil {
		return nil, err
	}
	cleanup := func() { s.discardStored(ctx, saved.StoragePath) }

	if saved.Size <= 0 {
		cleanup()
		return nil, domain.NewInvalid("file", "invalid_attachment_size", "attachment must not be empty")
	}
	if saved.Size > s.limits.MaxBytesPerFile {
		cleanup()
		return nil, attachmentTooLargeError(s.limits.MaxBytesPerFile)
	}
	if kind == domain.AttachmentText {
		if err := s.validateTextAttachment(ctx, saved.StoragePath); err != nil {
			cleanup()
			return nil, err
		}
	}
	duplicate, err := s.attachmentSHAExists(ctx, reqID, saved.SHA256)
	if err != nil {
		cleanup()
		return nil, err
	}
	if duplicate {
		cleanup()
		return nil, duplicateAttachmentError(saved.SHA256)
	}

	attachment := &Attachment{
		ID:            id,
		VersionID:     versionID,
		RequirementID: reqID,
		Kind:          kind,
		Filename:      safeName,
		MIMEType:      mimeType,
		SizeBytes:     saved.Size,
		SHA256:        saved.SHA256,
		StoragePath:   saved.StoragePath,
		CreatedBy:     actor,
	}
	if err := attachment.Validate(); err != nil {
		cleanup()
		return nil, err
	}
	if err := s.repo.CreateAttachment(ctx, attachment); err != nil {
		// The insert is the commit point for the metadata; a failure must not
		// leave an orphan file that no row points at.
		cleanup()
		return nil, err
	}

	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "attachment.added",
		Summary:          "attachment added",
		RefRequirementID: attachmentIDPtr(reqID),
		RefAttachmentID:  attachmentIDPtr(attachment.ID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail: map[string]any{
			"attachmentId":  strconv.FormatInt(attachment.ID, 10),
			"requirementId": strconv.FormatInt(reqID, 10),
			"kind":          attachment.Kind,
		},
	})
	return attachment, nil
}

// ReadText returns at most maxBytes (and never more than the 64 KiB per-file
// cap) of a text attachment. The result is always valid UTF-8: a rune split by
// the size bound is dropped. Non-text attachments and non-UTF-8 content are
// 422s (spec §6.7/§8.3).
func (s *AttachmentService) ReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, error) {
	if s.repo == nil || s.store == nil {
		return "", errors.New("version: attachment service is not configured")
	}
	attachment, err := s.loadScopedAttachment(ctx, versionID, 0, attachmentID)
	if err != nil {
		return "", err
	}
	if attachment.Kind != domain.AttachmentText {
		return "", unsupportedAttachmentTypeError("attachment %d is not a text attachment", attachmentID)
	}

	limit := s.limits.MaxTextBytes
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	if attachment.SizeBytes > 0 && attachment.SizeBytes < int64(limit) {
		limit = int(attachment.SizeBytes)
	}

	file, err := s.store.Open(ctx, attachment.StoragePath)
	if err != nil {
		return "", mapAttachmentFileError(attachmentID, err)
	}
	defer file.Close()

	data := make([]byte, limit)
	n, readErr := io.ReadFull(file, data)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("version: read attachment text: %w", readErr)
	}
	data = data[:n]
	if limit > 0 && n == limit {
		data = trimIncompleteUTF8Suffix(data)
	}
	if !utf8.Valid(data) {
		return "", domain.NewInvalid("file", "invalid_attachment_content",
			"text attachments must be valid UTF-8")
	}
	return string(data), nil
}

// ImageDataURL returns an image attachment as a data URL for the transient
// LLM-call resolution of spec §6.7/§8.3. It never writes the encoded value
// anywhere: the caller holds it only for the duration of the request. Only
// images at most 2 MiB qualify.
func (s *AttachmentService) ImageDataURL(ctx context.Context, attachmentID int64) (string, error) {
	if s.repo == nil || s.store == nil {
		return "", errors.New("version: attachment service is not configured")
	}
	attachment, err := s.repo.GetAttachment(ctx, attachmentID)
	if err != nil {
		return "", err
	}
	if attachment.Kind != domain.AttachmentImage {
		return "", unsupportedAttachmentTypeError("attachment %d is not an image attachment", attachmentID)
	}
	if attachment.SizeBytes > s.limits.MaxImageBytes {
		return "", attachmentTooLargeError(s.limits.MaxImageBytes)
	}
	file, err := s.store.Open(ctx, attachment.StoragePath)
	if err != nil {
		return "", mapAttachmentFileError(attachmentID, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, s.limits.MaxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("version: read attachment image: %w", err)
	}
	if int64(len(data)) > s.limits.MaxImageBytes {
		return "", attachmentTooLargeError(s.limits.MaxImageBytes)
	}
	return "data:" + attachment.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// Delete removes the metadata first and the file best effort afterwards (spec
// §6.7): a failed file removal leaves an orphan for the P8 GC but the delete
// itself succeeds. Only draft requirements may lose attachments; attachment
// ids are scoped to the requirement and version in the path.
func (s *AttachmentService) Delete(ctx context.Context, versionID, reqID, attachmentID int64, actor int64) error {
	if s.repo == nil || s.store == nil {
		return errors.New("version: attachment service is not configured")
	}
	requirement, err := s.loadRequirement(ctx, versionID, reqID)
	if err != nil {
		return err
	}
	if requirement.Status != domain.RequirementDraft {
		return requirementStateConflictError(reqID, requirement.Status)
	}
	attachment, err := s.loadScopedAttachment(ctx, versionID, reqID, attachmentID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAttachment(ctx, attachmentID); err != nil {
		return err
	}
	s.discardStored(ctx, attachment.StoragePath)

	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "attachment.removed",
		Summary:          "attachment removed",
		RefRequirementID: attachmentIDPtr(reqID),
		RefAttachmentID:  attachmentIDPtr(attachment.ID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail: map[string]any{
			"attachmentId":  strconv.FormatInt(attachment.ID, 10),
			"requirementId": strconv.FormatInt(reqID, 10),
			"kind":          attachment.Kind,
		},
	})
	return nil
}

// List returns the attachment metadata of one requirement scoped to versionID.
// It is an additive read helper for the HTTP layer (spec §7.3).
func (s *AttachmentService) List(ctx context.Context, versionID, reqID int64) ([]Attachment, error) {
	if s.repo == nil {
		return nil, errors.New("version: attachment service is not configured")
	}
	if _, err := s.loadRequirement(ctx, versionID, reqID); err != nil {
		return nil, err
	}
	return s.repo.ListAttachments(ctx, reqID)
}

// Get returns one attachment scoped to its requirement and version.
func (s *AttachmentService) Get(ctx context.Context, versionID, reqID, attachmentID int64) (*Attachment, error) {
	if s.repo == nil {
		return nil, errors.New("version: attachment service is not configured")
	}
	return s.loadScopedAttachment(ctx, versionID, reqID, attachmentID)
}

// OpenContent returns the attachment metadata and a seekable content stream for
// the download endpoint (spec §7.3). The caller closes the stream.
func (s *AttachmentService) OpenContent(ctx context.Context, versionID, reqID, attachmentID int64) (*Attachment, io.ReadSeekCloser, error) {
	if s.repo == nil || s.store == nil {
		return nil, nil, errors.New("version: attachment service is not configured")
	}
	attachment, err := s.loadScopedAttachment(ctx, versionID, reqID, attachmentID)
	if err != nil {
		return nil, nil, err
	}
	file, err := s.store.Open(ctx, attachment.StoragePath)
	if err != nil {
		return nil, nil, mapAttachmentFileError(attachmentID, err)
	}
	return attachment, file, nil
}

// loadRequirement fetches a requirement and rejects one that belongs to another
// version: a cross-version path id is indistinguishable from a missing one.
func (s *AttachmentService) loadRequirement(ctx context.Context, versionID, reqID int64) (*Requirement, error) {
	requirement, err := s.repo.Get(ctx, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.VersionID != versionID {
		return nil, domain.NewNotFound("requirement", reqID)
	}
	return requirement, nil
}

// loadScopedAttachment fetches an attachment and proves it belongs to versionID
// and, when reqID is positive, to requirementID.
func (s *AttachmentService) loadScopedAttachment(ctx context.Context, versionID, reqID, attachmentID int64) (*Attachment, error) {
	attachment, err := s.repo.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, err
	}
	if attachment.VersionID != versionID || (reqID > 0 && attachment.RequirementID != reqID) {
		return nil, domain.NewNotFound("attachment", attachmentID)
	}
	return attachment, nil
}

// checkAttachmentCounts enforces the per-requirement and per-version caps
// before any byte is written. The database has no count constraint; the small
// race between two concurrent uploads is accepted by the spec.
func (s *AttachmentService) checkAttachmentCounts(ctx context.Context, reqID, versionID int64) error {
	perRequirement, perVersion, err := s.repo.CountAttachments(ctx, reqID, versionID)
	if err != nil {
		return err
	}
	if perRequirement >= s.limits.MaxPerRequirement || perVersion >= s.limits.MaxPerVersion {
		return domain.NewInvalid("file", "attachment_limit_exceeded",
			fmt.Sprintf("a requirement allows at most %d attachments and a version at most %d",
				s.limits.MaxPerRequirement, s.limits.MaxPerVersion))
	}
	return nil
}

// attachmentSHAExists reports whether the requirement already carries an
// attachment with the same content hash (spec §6.7). The database unique index
// remains the race backstop and is mapped by the repository to
// duplicate_attachment.
func (s *AttachmentService) attachmentSHAExists(ctx context.Context, reqID int64, sha256 string) (bool, error) {
	attachments, err := s.repo.ListAttachments(ctx, reqID)
	if err != nil {
		return false, err
	}
	for _, attachment := range attachments {
		if attachment.SHA256 == sha256 {
			return true, nil
		}
	}
	return false, nil
}

// validateTextAttachment proves the stored text stream is valid UTF-8. Reading
// the committed file again keeps the write path streaming while still rejecting
// binary content labelled as text; the caller removes the file on failure.
func (s *AttachmentService) validateTextAttachment(ctx context.Context, storagePath string) error {
	file, err := s.store.Open(ctx, storagePath)
	if err != nil {
		return fmt.Errorf("version: open attachment for text validation: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("version: read attachment text: %w", err)
		}
		if r == utf8.RuneError && size == 1 {
			return domain.NewInvalid("file", "invalid_attachment_content",
				"text attachments must be valid UTF-8")
		}
	}
}

// discardStored removes a just-written file after a failed upload. Cleanup is
// best effort: if it fails the file is an orphan and the P8 GC owns it.
func (s *AttachmentService) discardStored(ctx context.Context, storagePath string) {
	if s.store == nil || storagePath == "" {
		return
	}
	if err := s.store.Remove(ctx, storagePath); err != nil {
		slog.WarnContext(ctx, "attachment file cleanup failed",
			"storage_path", storagePath,
			"error", err,
		)
	}
}

// publish emits one attachment event after a committed write. Publication
// failures are logged and swallowed (spec §6.10).
func (s *AttachmentService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "attachment event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// attachmentTypeForFilename derives the stored MIME type and kind from the
// sanitized filename's extension. The extension is the single source of truth
// for the stored MIME; the declared type and the content sniff are checks.
func attachmentTypeForFilename(filename string) (string, string, error) {
	ext := strings.ToLower(path.Ext(filename))
	mimeType, ok := attachmentCanonicalMIME[ext]
	if !ok {
		return "", "", unsupportedAttachmentTypeError(
			"attachment type must be one of txt, md, json, yaml, yml, png, jpg or jpeg")
	}
	kind, err := domain.KindForMIMEType(mimeType)
	if err != nil {
		return "", "", err
	}
	return mimeType, kind, nil
}

// validateDeclaredAttachmentType checks the client-declared multipart type
// against the extension. A missing type and the multipart default
// application/octet-stream are treated as "unspecified" and inferred; any other
// type outside the allow-list is rejected.
func validateDeclaredAttachmentType(declaredType, filename, kind string) error {
	declared := normalizeMIMEType(declaredType)
	if declared == "" || declared == "application/octet-stream" {
		return nil
	}
	declaredKind, err := domain.KindForMIMEType(declared)
	if err != nil {
		return unsupportedAttachmentTypeError("attachment type %q is not allowed", declared)
	}
	if declaredKind != kind {
		return unsupportedAttachmentTypeError(
			"attachment type %q does not match the %s attachment kind", declared, kind)
	}
	ext := strings.ToLower(path.Ext(filename))
	for _, allowed := range attachmentExtensionMIMEs[ext] {
		if allowed == declared {
			return nil
		}
	}
	return unsupportedAttachmentTypeError(
		"attachment type %q does not match the %q extension", declared, ext)
}

// normalizeMIMEType lower-cases a media type and drops its parameters.
func normalizeMIMEType(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(raw); err == nil {
		return strings.ToLower(mediaType)
	}
	if idx := strings.IndexByte(raw, ';'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// detectAttachmentMIME sniffs the content prefix with http.DetectContentType and
// strips the charset parameters of text results.
func detectAttachmentMIME(head []byte) string {
	detected := http.DetectContentType(head)
	if mediaType, _, err := mime.ParseMediaType(detected); err == nil {
		return mediaType
	}
	return strings.ToLower(detected)
}

// validateDetectedAttachmentMIME proves the sniffed content belongs to the
// extension's kind: an image must be exactly the expected image type, text must
// sniff as a text media type. A contradiction is a 422 invalid_attachment_content.
func validateDetectedAttachmentMIME(detected, expectedMIME, kind string) error {
	if kind == domain.AttachmentImage {
		if detected != expectedMIME {
			return domain.NewInvalid("file", "invalid_attachment_content",
				fmt.Sprintf("attachment content is %q, expected %q", detected, expectedMIME))
		}
		return nil
	}
	if !strings.HasPrefix(detected, "text/") {
		return domain.NewInvalid("file", "invalid_attachment_content",
			fmt.Sprintf("attachment content is %q, which is not text", detected))
	}
	return nil
}

// sanitizeAttachmentFilename reduces a user filename to a single safe path
// component: the basename, Unicode preserved, path separators/control
// characters/colon removed, surrounding and trailing spaces/dots trimmed, at
// most 120 runes with the extension kept (spec §6.7).
func sanitizeAttachmentFilename(raw string) (string, error) {
	name := strings.ReplaceAll(raw, `\`, "/")
	name = path.Base(name)

	var builder strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || strings.ContainsRune(`*?"<>|`, r) || unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
	}
	name = strings.TrimSpace(builder.String())
	// Windows strips trailing dots and spaces from file names; doing it here
	// keeps the DB filename and the on-disk name in agreement.
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." {
		return "", invalidFilenameError("filename is required")
	}

	ext := path.Ext(name)
	if ext != "" && strings.TrimSpace(strings.TrimSuffix(name, ext)) == "" {
		return "", invalidFilenameError("filename must have a name before its extension")
	}
	if utf8.RuneCountInString(name) > domain.MaxAttachmentFilenameChars {
		name = truncateAttachmentFilename(name, domain.MaxAttachmentFilenameChars)
	}
	if len(name) > maxAttachmentFilenameBytes {
		name = truncateAttachmentFilenameBytes(name, maxAttachmentFilenameBytes)
	}
	if name == "" || name == "." || name == ".." {
		return "", invalidFilenameError("filename is required")
	}
	return name, nil
}

// truncateAttachmentFilename shortens name to at most max runes while keeping
// its extension, so the extension allow-list still sees the original type.
func truncateAttachmentFilename(name string, max int) string {
	runes := []rune(name)
	if len(runes) <= max {
		return name
	}
	ext := path.Ext(name)
	extRunes := []rune(ext)
	stemRunes := []rune(strings.TrimSuffix(name, ext))
	keep := max - len(extRunes)
	if keep <= 0 {
		return string(runes[:max])
	}
	if len(stemRunes) > keep {
		stemRunes = stemRunes[:keep]
	}
	stem := strings.TrimRight(string(stemRunes), ". ")
	return stem + ext
}

// truncateAttachmentFilenameBytes shortens a filename to at most maxBytes of
// UTF-8 while keeping its extension, for multi-byte names that fit the rune cap
// but not the filesystem's component byte/UTF-16 limit.
func truncateAttachmentFilenameBytes(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	ext := path.Ext(name)
	runes := []rune(strings.TrimSuffix(name, ext))
	keep := maxBytes - len(ext)
	if keep <= 0 {
		runes = []rune(name)
		keep = maxBytes
	}
	for i := len(runes); i > 0; i-- {
		candidate := string(runes[:i])
		if len(candidate) > keep {
			continue
		}
		candidate = strings.TrimRight(candidate, ". ")
		if candidate == "" {
			return ext
		}
		return candidate + ext
	}
	return ext
}

// trimIncompleteUTF8Suffix removes a trailing partial rune from data so a
// size-bounded read can be decoded as text. A complete but invalid byte
// sequence is returned unchanged and rejected by the caller.
func trimIncompleteUTF8Suffix(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}
	for i := 1; i <= utf8.UTFMax && i <= len(data); i++ {
		index := len(data) - i
		if !utf8.RuneStart(data[index]) {
			continue
		}
		if !utf8.FullRune(data[index:]) {
			return data[:index]
		}
		return data
	}
	return data
}

// mapAttachmentFileError maps a missing attachment file to a 404 while other
// filesystem failures stay infrastructure errors.
func mapAttachmentFileError(attachmentID int64, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return domain.NewNotFound("attachment", attachmentID)
	}
	return err
}

// unsupportedAttachmentTypeError builds the spec §7.1 422
// unsupported_attachment_type.
func unsupportedAttachmentTypeError(format string, args ...any) error {
	return domain.NewInvalid("file", "unsupported_attachment_type", fmt.Sprintf(format, args...))
}

// invalidFilenameError builds the spec §7.1 422 invalid_filename.
func invalidFilenameError(message string) error {
	return domain.NewInvalid("filename", "invalid_filename", message)
}

// attachmentTooLargeError builds the spec §7.1 422 attachment_too_large.
func attachmentTooLargeError(max int64) error {
	return domain.NewInvalid("file", "attachment_too_large",
		fmt.Sprintf("attachment must be at most %d bytes", max))
}

// duplicateAttachmentError builds the spec §7.1 409 duplicate_attachment.
func duplicateAttachmentError(sha256 string) error {
	return domain.NewConflict("duplicate_attachment",
		fmt.Sprintf("this requirement already has an attachment with sha256 %s", sha256))
}

// attachmentIDPtr returns a pointer to id for a VersionEvent reference.
func attachmentIDPtr(id int64) *int64 { return &id }
