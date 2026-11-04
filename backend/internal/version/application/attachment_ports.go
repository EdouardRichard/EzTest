package versionapp

import (
	"context"
	"io"
)

// ContentObject is the metadata of one content-ledger object.
type ContentObject struct {
	SHA256 string
	Size   int64
	MIME   string
}

// ContentRef is one reference from an owning entity to a content object.
type ContentRef struct {
	ID        int64
	ObjectID  string
	OwnerKind string
	OwnerID   int64
	Role      string
}

// ContentStore is the content-ledger port used by attachments. The storage
// application service is adapted to it by infrastructure; the version context
// never opens the content files itself.
type ContentStore interface {
	Open(ctx context.Context, sha256 string) (ContentObject, io.ReadCloser, error)
	AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (ContentRef, error)
	ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]ContentRef, error)
	Upload(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (ContentObject, error)
}

// TextExtractor extracts plain text from attachment content.
type TextExtractor interface {
	Extract(ctx context.Context, name, mime string, r io.Reader) (string, error)
}

// ImageProber sniffs image bytes and returns the detected MIME plus the pixel
// dimensions. It is implemented by the attachments infrastructure (PNG/JPEG/
// WebP header probing); the application never decodes image pixels itself.
type ImageProber interface {
	Probe(ctx context.Context, declaredMIME string, data []byte) (mime string, width, height int, err error)
}
