// Package storageapp implements the content-object application service:
// uploads with SHA-256 de-duplication, verified downloads and the reference
// ledger used by later phases.
package storageapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

const (
	// MaxUploadBytes is the hard content upload limit (10 MiB).
	MaxUploadBytes int64 = 10 << 20
	// DataRootSettingKey is the settings key holding the content root.
	DataRootSettingKey = "paths.data_root"
	// DefaultRefRole is used when a reference does not name a role.
	DefaultRefRole = "attachment"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256 reports whether value is a lowercase hex SHA-256.
func ValidSHA256(value string) bool {
	return sha256Pattern.MatchString(value)
}

// Service stores content objects and their references. audit and events are
// best-effort sinks and may be nil in unit tests.
type Service struct {
	repo     storage.Repository
	files    FileStore
	settings SettingsReader
	ids      id.Generator
	audit    auditapp.Recorder
	events   eventingapp.Publisher
}

// New wires the content service.
func New(
	repo storage.Repository,
	files FileStore,
	settings SettingsReader,
	ids id.Generator,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	return &Service{repo: repo, files: files, settings: settings, ids: ids, audit: audit, events: events}
}

// Upload stores a content blob. filename and declaredMIME are transport
// metadata only: the original name is never persisted and the MIME type is
// always detected from the bytes (http.DetectContentType), per the design.
func (s *Service) Upload(ctx context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error) {
	if r == nil {
		return storage.Object{}, platform.Invalid("file", "file content is required")
	}
	if maxBytes <= 0 || maxBytes > MaxUploadBytes {
		maxBytes = MaxUploadBytes
	}
	root, err := s.dataRoot(ctx)
	if err != nil {
		return storage.Object{}, err
	}
	data, err := readUpload(r, maxBytes)
	if err != nil {
		return storage.Object{}, err
	}

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	existing, err := s.repo.GetObject(ctx, sha)
	if err == nil {
		s.recordUpload(ctx, existing, actorID)
		return existing, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Object{}, err
	}

	relativePath, err := s.files.Put(ctx, root, sha, data)
	if err != nil {
		return storage.Object{}, err
	}
	object := storage.Object{
		SHA256:       sha,
		Size:         int64(len(data)),
		MIME:         http.DetectContentType(data),
		RelativePath: relativePath,
	}
	stored, _, err := s.repo.PutObject(ctx, object)
	if err != nil {
		return storage.Object{}, err
	}
	s.recordUpload(ctx, stored, actorID)
	return stored, nil
}

// Open returns a content object and a reader over verified bytes. The object
// row must exist and the stored bytes must still hash to the object key and
// match its recorded size; otherwise ErrCorrupt is returned.
func (s *Service) Open(ctx context.Context, sha string) (storage.Object, io.ReadCloser, error) {
	if !ValidSHA256(sha) {
		return storage.Object{}, nil, platform.Invalid("sha256", "sha256 must be 64 lowercase hex characters")
	}
	object, err := s.repo.GetObject(ctx, sha)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.Object{}, nil, platform.NotFound("content")
		}
		return storage.Object{}, nil, err
	}
	root, err := s.dataRoot(ctx)
	if err != nil {
		return storage.Object{}, nil, err
	}
	reader, err := s.files.Open(ctx, root, object.RelativePath)
	if err != nil {
		return storage.Object{}, nil, fmt.Errorf("open stored content %s: %w", sha, err)
	}
	defer reader.Close()

	limit := object.Size + 1
	if limit <= 0 || limit > MaxUploadBytes+1 {
		limit = MaxUploadBytes + 1
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit))
	if err != nil {
		return storage.Object{}, nil, fmt.Errorf("read stored content %s: %w", sha, err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != object.Size || hex.EncodeToString(sum[:]) != object.SHA256 {
		return storage.Object{}, nil, fmt.Errorf("%w: content %s", storage.ErrCorrupt, sha)
	}
	return object, io.NopCloser(bytes.NewReader(data)), nil
}

// AddRef records that an owner uses a content object. A blank role defaults to
// DefaultRefRole.
func (s *Service) AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error) {
	objectID, ownerKind, ownerID, role, err := s.validateRef(ctx, objectID, ownerKind, ownerID, role)
	if err != nil {
		return storage.Ref{}, err
	}
	if s.ids == nil {
		return storage.Ref{}, platform.NewError(http.StatusInternalServerError, "internal", "id generator is not configured")
	}
	return s.repo.AddRef(ctx, storage.Ref{
		ID:        s.ids.Next(),
		ObjectID:  objectID,
		OwnerKind: ownerKind,
		OwnerID:   ownerID,
		Role:      role,
	})
}

// ReplaceRef records objectID as the single active reference of
// (ownerKind, ownerID, role). It is the idempotent write used by renderers that
// may re-run (report PDFs): a repeat replaces/reuses the existing ref instead
// of appending a duplicate. The role is required; a blank role keeps its
// AddRef default.
func (s *Service) ReplaceRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error) {
	objectID, ownerKind, ownerID, role, err := s.validateRef(ctx, objectID, ownerKind, ownerID, role)
	if err != nil {
		return storage.Ref{}, err
	}
	if s.ids == nil {
		return storage.Ref{}, platform.NewError(http.StatusInternalServerError, "internal", "id generator is not configured")
	}
	return s.repo.ReplaceRef(ctx, storage.Ref{
		ID:        s.ids.Next(),
		ObjectID:  objectID,
		OwnerKind: ownerKind,
		OwnerID:   ownerID,
		Role:      role,
	})
}

// validateRef shares the content reference argument validation of AddRef and
// ReplaceRef.
func (s *Service) validateRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (string, string, int64, string, error) {
	if !ValidSHA256(objectID) {
		return "", "", 0, "", platform.Invalid("object_id", "object_id must be a lowercase hex SHA-256")
	}
	ownerKind = strings.TrimSpace(ownerKind)
	if err := validation.String("owner_kind", ownerKind, 1, 64); err != nil {
		return "", "", 0, "", err
	}
	if ownerID <= 0 {
		return "", "", 0, "", platform.Invalid("owner_id", "owner_id must be a positive decimal ID")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = DefaultRefRole
	}
	if err := validation.String("role", role, 1, 64); err != nil {
		return "", "", 0, "", err
	}
	if _, err := s.repo.GetObject(ctx, objectID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", "", 0, "", platform.NotFound("content")
		}
		return "", "", 0, "", err
	}
	return objectID, ownerKind, ownerID, role, nil
}

// ListRefs lists the active references of one owner.
func (s *Service) ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]storage.Ref, error) {
	ownerKind = strings.TrimSpace(ownerKind)
	if err := validation.String("owner_kind", ownerKind, 1, 64); err != nil {
		return nil, err
	}
	if ownerID <= 0 {
		return nil, platform.Invalid("owner_id", "owner_id must be a positive decimal ID")
	}
	return s.repo.ListRefs(ctx, ownerKind, ownerID)
}

// SoftDeleteRef soft-deletes a reference.
func (s *Service) SoftDeleteRef(ctx context.Context, refID int64) error {
	if refID <= 0 {
		return platform.Invalid("id", "ref id must be a positive decimal ID")
	}
	if err := s.repo.SoftDeleteRef(ctx, refID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return platform.NotFound("content_ref")
		}
		return err
	}
	return nil
}

func (s *Service) dataRoot(ctx context.Context) (string, error) {
	if s.settings == nil {
		return "", platform.NewError(http.StatusInternalServerError, "internal", "settings reader is not configured")
	}
	root, err := s.settings.String(ctx, DataRootSettingKey)
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", platform.NewError(http.StatusInternalServerError, "internal", "paths.data_root is not configured")
	}
	return root, nil
}

func (s *Service) recordUpload(ctx context.Context, object storage.Object, actorID int64) {
	requestID := requestid.From(ctx)
	metadata := map[string]any{"sha256": object.SHA256, "size": object.Size, "mime": object.MIME}
	if s.audit != nil {
		actor := actorID
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    &actor,
			Action:     "content.uploaded",
			TargetKind: "content_object",
			TargetID:   object.SHA256,
			RequestID:  requestID,
			Metadata:   metadata,
		})
	}
	if s.events != nil {
		actor := actorID
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      "content.uploaded",
			Scope:     map[string]any{},
			Payload:   metadata,
			ActorID:   &actor,
			RequestID: requestID,
		})
	}
}

func readUpload(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, platform.NewError(http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("content exceeds the %d MiB limit", maxBytes>>20))
	}
	return data, nil
}
