package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	versionagent "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/agent"
)

// fakeImageAttachments records image data URL resolutions.
type fakeImageAttachments struct {
	dataURL string
	err     error
	ids     []int64
}

// ImageDataURL records the attachment id and returns the configured result.
func (f *fakeImageAttachments) ImageDataURL(_ context.Context, attachmentID int64) (string, error) {
	f.ids = append(f.ids, attachmentID)
	if f.err != nil {
		return "", f.err
	}
	return f.dataURL, nil
}

// TestImageResolverReturnsDataURLAndRejectsNonImage pins the P1 ImageResolver
// adapter: an attachment:<id> reference resolves through the attachment service
// (which enforces the image kind and size), and malformed or non-attachment
// references are rejected without touching the filesystem.
func TestImageResolverReturnsDataURLAndRejectsNonImage(t *testing.T) {
	ctx := context.Background()
	const dataURL = "data:image/png;base64,AAAA"

	t.Run("attachment reference resolves to a data URL", func(t *testing.T) {
		attachments := &fakeImageAttachments{dataURL: dataURL}
		resolver := versionagent.NewImageResolver(attachments)
		got, err := resolver.ResolveImage(ctx, "attachment:42")
		if err != nil {
			t.Fatalf("ResolveImage: %v", err)
		}
		if got != dataURL {
			t.Fatalf("ResolveImage=%q want %q", got, dataURL)
		}
		if len(attachments.ids) != 1 || attachments.ids[0] != 42 {
			t.Fatalf("resolved ids=%v want [42]", attachments.ids)
		}
	})

	t.Run("non-image attachment failure is propagated", func(t *testing.T) {
		attachments := &fakeImageAttachments{err: errors.New("attachment 43 is not an image attachment")}
		resolver := versionagent.NewImageResolver(attachments)
		if _, err := resolver.ResolveImage(ctx, "attachment:43"); err == nil ||
			!strings.Contains(err.Error(), "not an image") {
			t.Fatalf("ResolveImage(non-image) error=%v", err)
		}
	})

	t.Run("malformed references are rejected before the lookup", func(t *testing.T) {
		attachments := &fakeImageAttachments{dataURL: dataURL}
		resolver := versionagent.NewImageResolver(attachments)
		for _, ref := range []string{
			"", "attachment", "attachment:", "attachment:abc", "attachment:0",
			"attachment:-1", "attachment:+1", "attachment:42x", "attachment:42 ",
			"file:42", "ATTACHMENT:42", " attachment:42",
		} {
			if _, err := resolver.ResolveImage(ctx, ref); err == nil {
				t.Fatalf("ResolveImage(%q) returned nil error", ref)
			}
		}
		if len(attachments.ids) != 0 {
			t.Fatalf("malformed references reached the attachment service: %v", attachments.ids)
		}
	})

	t.Run("a nil attachment service is rejected", func(t *testing.T) {
		if _, err := versionagent.NewImageResolver(nil).ResolveImage(ctx, "attachment:42"); err == nil {
			t.Fatal("ResolveImage(nil service) returned nil error")
		}
	})
}
