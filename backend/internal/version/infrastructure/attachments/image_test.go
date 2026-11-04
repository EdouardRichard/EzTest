package attachments_test

import (
	"context"
	"encoding/binary"
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

func TestImageProberDetectsDimensions(t *testing.T) {
	prober := attachments.NewImageProber()
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
			mime, width, height, err := prober.Probe(context.Background(), tc.mime, tc.data)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if mime != tc.want || width != tc.width || height != tc.height {
				t.Fatalf("probe = %q %dx%d, want %q %dx%d", mime, width, height, tc.want, tc.width, tc.height)
			}
		})
	}
}

func TestImageProberRejectsInvalidAndOversized(t *testing.T) {
	prober := attachments.NewImageProber()
	if _, _, _, err := prober.Probe(context.Background(), "image/png", []byte("not an image")); err == nil {
		t.Fatal("garbage accepted as an image")
	}
	if _, _, _, err := prober.Probe(context.Background(), "image/png", pngFixture(1, 1)[:20]); err == nil {
		t.Fatal("truncated PNG accepted")
	}
	if _, _, _, err := prober.Probe(context.Background(), "image/png", pngFixture(20000, 16)); err == nil {
		t.Fatal("oversized dimension accepted")
	}
	if _, _, _, err := prober.Probe(context.Background(), "image/webp", pngFixture(800, 600)); err == nil {
		t.Fatal("png accepted under a webp declaration")
	}
}
