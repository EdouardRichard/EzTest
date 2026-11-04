// Package attachments image helpers detect the image format and pixel
// dimensions of an upload. Images are never OCRed or converted to text: the
// vision model reads the original bytes referenced by the image part's
// data_ref (spec §8.3).
package attachments

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ImageProber implements versionapp.ImageProber: it sniffs png/jpeg/webp from
// magic bytes, verifies the declared MIME (when supported) matches the content
// and rejects oversized images instead of silently downscaling them (§8.3).
// It lives in infrastructure so the version application never imports this
// package (R25).
type ImageProber struct{}

// NewImageProber builds the attachment image prober.
func NewImageProber() *ImageProber { return &ImageProber{} }

// Probe returns the detected MIME plus the pixel dimensions of one image.
func (ImageProber) Probe(_ context.Context, declaredMIME string, data []byte) (string, int, int, error) {
	detected, width, height, err := ImageDimensions(data)
	if err != nil {
		// Header probing failures are user input errors (corrupt or unsupported
		// image), not infrastructure failures: surface a field-level 400.
		return "", 0, 0, platform.Invalid("mime", "image content is not a supported png, jpeg or webp file")
	}
	declared := strings.ToLower(strings.TrimSpace(declaredMIME))
	if declared == "image/jpg" {
		declared = "image/jpeg"
	}
	if version.IsSupportedImageMIME(declared) && declared != detected {
		return "", 0, 0, platform.Invalid("mime",
			"declared mime "+declaredMIME+" does not match the image content ("+detected+")")
	}
	if err := version.ValidateImageDimensions(width, height); err != nil {
		return "", 0, 0, err
	}
	return detected, width, height, nil
}

// ImageDimensions detects png/jpeg/webp from magic bytes and returns the pixel
// dimensions. It reads headers only; the full image is not decoded.
func ImageDimensions(data []byte) (mime string, width, height int, err error) {
	switch {
	case len(data) >= 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return pngDimensions(data)
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		return jpegDimensions(data)
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webpDimensions(data)
	default:
		return "", 0, 0, fmt.Errorf("%w: image content is not png, jpeg or webp", version.ErrUnsupportedAttachmentFormat)
	}
}

func pngDimensions(data []byte) (string, int, int, error) {
	if len(data) < 24 || string(data[12:16]) != "IHDR" {
		return "", 0, 0, fmt.Errorf("%w: truncated png header", version.ErrAttachmentCorrupt)
	}
	width := int(binary.BigEndian.Uint32(data[16:20]))
	height := int(binary.BigEndian.Uint32(data[20:24]))
	return "image/png", width, height, nil
}

func jpegDimensions(data []byte) (mime string, width, height int, err error) {
	position := 2
	for position+4 <= len(data) {
		if data[position] != 0xFF {
			position++
			continue
		}
		marker := data[position+1]
		// Standalone markers have no length field.
		if marker == 0xD8 || marker == 0xD9 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			position += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[position+2 : position+4]))
		if length < 2 {
			return "", 0, 0, fmt.Errorf("%w: malformed jpeg segment", version.ErrAttachmentCorrupt)
		}
		if isJPEGStartOfFrame(marker) {
			if position+9 > len(data) {
				return "", 0, 0, fmt.Errorf("%w: truncated jpeg frame header", version.ErrAttachmentCorrupt)
			}
			height = int(binary.BigEndian.Uint16(data[position+5 : position+7]))
			width = int(binary.BigEndian.Uint16(data[position+7 : position+9]))
			return "image/jpeg", width, height, nil
		}
		position += 2 + length
	}
	return "", 0, 0, fmt.Errorf("%w: jpeg frame header not found", version.ErrAttachmentCorrupt)
}

func isJPEGStartOfFrame(marker byte) bool {
	switch marker {
	case 0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF:
		return true
	default:
		return false
	}
}

func webpDimensions(data []byte) (mime string, width, height int, err error) {
	position := 12
	for position+8 <= len(data) {
		fourcc := string(data[position : position+4])
		size := int(binary.LittleEndian.Uint32(data[position+4 : position+8]))
		payload := position + 8
		if size < 0 || payload+size > len(data) {
			return "", 0, 0, fmt.Errorf("%w: truncated webp chunk", version.ErrAttachmentCorrupt)
		}
		switch fourcc {
		case "VP8 ":
			if size < 10 || data[payload+3] != 0x9D || data[payload+4] != 0x01 || data[payload+5] != 0x2A {
				return "", 0, 0, fmt.Errorf("%w: malformed lossy webp frame", version.ErrAttachmentCorrupt)
			}
			width = int(binary.LittleEndian.Uint16(data[payload+6:payload+8]) & 0x3FFF)
			height = int(binary.LittleEndian.Uint16(data[payload+8:payload+10]) & 0x3FFF)
			return "image/webp", width, height, nil
		case "VP8L":
			if size < 5 || data[payload] != 0x2F {
				return "", 0, 0, fmt.Errorf("%w: malformed lossless webp frame", version.ErrAttachmentCorrupt)
			}
			bits := binary.LittleEndian.Uint32(data[payload+1 : payload+5])
			width = int(bits&0x3FFF) + 1
			height = int((bits>>14)&0x3FFF) + 1
			return "image/webp", width, height, nil
		case "VP8X":
			if size < 10 {
				return "", 0, 0, fmt.Errorf("%w: truncated extended webp header", version.ErrAttachmentCorrupt)
			}
			width = int(readUint24LE(data[payload+4:payload+7])) + 1
			height = int(readUint24LE(data[payload+7:payload+10])) + 1
			return "image/webp", width, height, nil
		}
		position = payload + size
		if size%2 == 1 {
			position++ // RIFF chunks are word aligned
		}
	}
	return "", 0, 0, fmt.Errorf("%w: webp image header not found", version.ErrAttachmentCorrupt)
}

func readUint24LE(data []byte) uint32 {
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
}
