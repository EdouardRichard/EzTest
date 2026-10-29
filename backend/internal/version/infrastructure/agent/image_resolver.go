package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// imageRefPrefix is the only image reference form the P3 attachment service can
// resolve (design spec §6.7: attachment:<id>).
const imageRefPrefix = "attachment:"

// ImageAttachmentReader is the narrow slice of the P3 attachment service the P1
// ImageResolver adapter needs. *version/application.AttachmentService satisfies
// it.
type ImageAttachmentReader interface {
	ImageDataURL(ctx context.Context, attachmentID int64) (string, error)
}

// ImageResolver implements the P1 ImageResolver port by reading an image
// attachment as a transient data URL (design spec §6.7/§8.2). The value is
// generated for one LLM call and must never be persisted or logged (D30).
type ImageResolver struct {
	attachments ImageAttachmentReader
}

// NewImageResolver wires the resolver to the P3 attachment service.
func NewImageResolver(attachments ImageAttachmentReader) *ImageResolver {
	return &ImageResolver{attachments: attachments}
}

var _ agentapp.ImageResolver = (*ImageResolver)(nil)

// ResolveImage parses attachment:<id> and returns the attachment service's data
// URL. A malformed reference, an attachment that is not a supported image and
// an over-limit image are all errors.
func (r *ImageResolver) ResolveImage(ctx context.Context, ref string) (string, error) {
	attachmentID, err := parseAttachmentReference(ref)
	if err != nil {
		return "", err
	}
	if r == nil || r.attachments == nil {
		return "", errors.New("version: attachment service is not configured")
	}
	return r.attachments.ImageDataURL(ctx, attachmentID)
}

// parseAttachmentReference accepts exactly "attachment:<positive decimal id>".
func parseAttachmentReference(ref string) (int64, error) {
	invalid := fmt.Errorf("version: unsupported image reference %q, want attachment:<id>", ref)
	if len(ref) <= len(imageRefPrefix) || ref[:len(imageRefPrefix)] != imageRefPrefix {
		return 0, invalid
	}
	digits := ref[len(imageRefPrefix):]
	if len(digits) > 19 {
		return 0, invalid
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, invalid
		}
	}
	attachmentID, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || attachmentID <= 0 {
		return 0, invalid
	}
	return attachmentID, nil
}
