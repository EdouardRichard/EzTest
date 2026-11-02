package attachments_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

func pngFixture(width, height int) []byte {
	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	header := make([]byte, 25)
	binary.BigEndian.PutUint32(header[0:4], 13)
	copy(header[4:8], "IHDR")
	binary.BigEndian.PutUint32(header[8:12], uint32(width))
	binary.BigEndian.PutUint32(header[12:16], uint32(height))
	header[16] = 8 // bit depth
	header[17] = 2 // truecolor
	return append(data, header...)
}

func jpegFixture(width, height int) []byte {
	data := []byte{0xFF, 0xD8}
	app0 := []byte{0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00}
	data = append(data, app0...)
	sof := []byte{
		0xFF, 0xC0, 0x00, 0x11, 0x08, 0, 0, 0, 0, 0x03,
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
	binary.BigEndian.PutUint16(sof[5:7], uint16(height))
	binary.BigEndian.PutUint16(sof[7:9], uint16(width))
	return append(data, sof...)
}

func webpChunk(fourcc string, payload []byte) []byte {
	chunk := make([]byte, 8+len(payload))
	copy(chunk[0:4], fourcc)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(payload)))
	copy(chunk[8:], payload)
	body := append([]byte("WEBP"), chunk...)
	data := make([]byte, 8+len(body))
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(body)))
	copy(data[8:], body)
	return data
}

func webpLossyFixture(width, height int) []byte {
	payload := make([]byte, 10)
	copy(payload[3:6], []byte{0x9D, 0x01, 0x2A})
	binary.LittleEndian.PutUint16(payload[6:8], uint16(width)&0x3FFF)
	binary.LittleEndian.PutUint16(payload[8:10], uint16(height)&0x3FFF)
	return webpChunk("VP8 ", payload)
}

func webpExtendedFixture(width, height int) []byte {
	payload := make([]byte, 10)
	payload[0] = 0x10
	putUint24LE(payload[4:7], uint32(width-1))
	putUint24LE(payload[7:10], uint32(height-1))
	return webpChunk("VP8X", payload)
}

func putUint24LE(dst []byte, value uint32) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
}

func TestBuildImagePartProbesDimensions(t *testing.T) {
	shasum := strings.Repeat("a", 64)
	cases := []struct {
		name   string
		mime   string
		data   []byte
		width  int
		height int
		want   string
	}{
		{"png", "image/png", pngFixture(800, 600), 800, 600, "image/png"},
		{"jpeg", "image/jpeg", jpegFixture(1280, 720), 1280, 720, "image/jpeg"},
		{"webp lossy", "image/webp", webpLossyFixture(640, 480), 640, 480, "image/webp"},
		{"webp extended", "image/webp", webpExtendedFixture(1024, 768), 1024, 768, "image/webp"},
		{"mime inferred from magic", "application/octet-stream", pngFixture(32, 16), 32, 16, "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			part, err := attachments.BuildImagePart(tc.mime, shasum, tc.data)
			if err != nil {
				t.Fatalf("BuildImagePart: %v", err)
			}
			if part.Mime != tc.want || part.DataRef != shasum || part.Width != tc.width || part.Height != tc.height {
				t.Fatalf("part = %#v", part)
			}
		})
	}
}

func TestBuildImagePartRejectsInvalidAndOversized(t *testing.T) {
	shasum := strings.Repeat("a", 64)
	if _, err := attachments.BuildImagePart("image/png", shasum, []byte("not an image")); err == nil {
		t.Fatal("garbage accepted as an image")
	}
	if _, err := attachments.BuildImagePart("image/png", shasum, pngFixture(1, 1)[:20]); err == nil {
		t.Fatal("truncated PNG accepted")
	}
	if _, err := attachments.BuildImagePart("image/png", shasum, pngFixture(20000, 16)); err == nil {
		t.Fatal("oversized dimension accepted")
	}
	if _, err := attachments.BuildImagePart("image/webp", shasum, pngFixture(800, 600)); err == nil {
		t.Fatal("png accepted under a webp declaration")
	}
	if _, err := attachments.BuildImagePart("image/png", "", pngFixture(8, 8)); err == nil {
		t.Fatal("empty data_ref accepted")
	}
}
