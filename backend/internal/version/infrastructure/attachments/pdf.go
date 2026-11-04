package attachments

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/ledongthuc/pdf"
)

// PDFPageSeparator separates the text extracted from consecutive PDF pages. It
// is a visible marker rather than a bare newline so readers (and the LLM
// context built from the attachment text) can tell where one page ends.
const PDFPageSeparator = "\n\n=== page break ===\n\n"

// ErrPDFNoTextLayer reports a PDF without any extractable text layer, e.g. a
// scanned or image-only document. Embedded images are extracted for PDFs that
// do have a text layer, but a scanned PDF is rejected instead of yielding empty
// text that looks like a successful extraction; no OCR or page rendering is
// performed (spec N-7, R-A7-3).
var ErrPDFNoTextLayer = errors.New("pdf has no text layer")

// extractPDFResult extracts positioned text from at most MaxPDFPages pages plus
// every parseable embedded image XObject. Text runs are decoded through each
// font's ToUnicode CMap, which covers the Type0/Identity-H CID fonts Chinese
// documents use. Images whose filter/color space cannot be decoded are reported
// as skipped with a reason; page rendering and OCR are never performed.
func extractPDFResult(data []byte) (result versionapp.ExtractionResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = versionapp.ExtractionResult{}
			err = fmt.Errorf("%w: malformed PDF", ErrCorrupt)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	pages := reader.NumPage()
	if pages > version.MaxPDFPages {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: PDF has %d pages (limit %d)", ErrTooLarge, pages, version.MaxPDFPages)
	}
	if pages <= 0 {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: PDF has no pages", ErrCorrupt)
	}
	type pageText struct {
		number int
		text   string
	}
	extracted := make([]pageText, 0, pages)
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		// Passing nil builds the font cache per page: PDF resource names
		// such as /F1 are page scoped, so sharing one map across pages could
		// decode a run with another page's font.
		text, pageErr := page.GetPlainText(nil)
		if pageErr != nil {
			return versionapp.ExtractionResult{}, fmt.Errorf("%w: PDF page %d: %v", ErrCorrupt, pageNumber, pageErr)
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		extracted = append(extracted, pageText{number: pageNumber, text: text})
	}
	if len(extracted) == 0 {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: scanned or image-only PDFs are not supported", ErrPDFNoTextLayer)
	}
	builder := newResultBuilder()
	for index, page := range extracted {
		if index > 0 {
			if err := builder.addText(PDFPageSeparator); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
		if err := builder.addText(page.text); err != nil {
			return versionapp.ExtractionResult{}, err
		}
		if err := builder.element(versionapp.ExtractionElement{
			Kind: "page", Text: page.text, Page: page.number, Source: fmt.Sprintf("pdf:page:%d", page.number),
		}); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	images, skipped, scanErr := scanPDFImages(data)
	if scanErr != nil {
		return versionapp.ExtractionResult{}, scanErr
	}
	for _, image := range images {
		if err := builder.image(image); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	for _, skip := range skipped {
		if err := builder.skip("image", skip.source, skip.reason); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	return builder.result(), nil
}

// ---------- embedded image objects ----------

type pdfSkip struct {
	source string
	reason string
}

type pdfImageObject struct {
	number  string
	dict    []byte
	stream  []byte
	width   int
	height  int
	bits    int
	filters []string
}

var (
	pdfObjectPattern      = regexp.MustCompile(`(?s)(\d+)\s+\d+\s+obj\b`)
	pdfImageSubtype       = regexp.MustCompile(`/Subtype\s*/Image\b`)
	pdfWidthPattern       = regexp.MustCompile(`/Width\s+(\d+)`)
	pdfHeightPattern      = regexp.MustCompile(`/Height\s+(\d+)`)
	pdfBitsPattern        = regexp.MustCompile(`/BitsPerComponent\s+(\d+)`)
	pdfLengthPattern      = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)
	pdfFilterPattern      = regexp.MustCompile(`/Filter\s*(\[[^\]]*\]|/[^\s/<>\[\]]+)`)
	pdfColorSpacePattern  = regexp.MustCompile(`/ColorSpace\s*(\[[^\]]*\]|/[^\s/<>\[\]]+)`)
	pdfFilterNamePattern  = regexp.MustCompile(`/([A-Za-z0-9]+)`)
	pdfUnsupportedFilters = map[string]bool{
		"JPXDecode": true, "CCITTFaxDecode": true, "JBIG2Decode": true,
		"LZWDecode": true, "RunLengthDecode": true, "Crypt": true,
	}
)

// scanPDFImages finds image XObjects by scanning the raw PDF object table. The
// reader library exposes only text, so the object streams are decoded here.
// Parseable image objects become image refs; everything else is returned as an
// explicit skip with its reason (R-A7-3a).
func scanPDFImages(data []byte) ([]versionapp.ExtractionImage, []pdfSkip, error) {
	matches := pdfObjectPattern.FindAllSubmatchIndex(data, -1)
	if len(matches) == 0 {
		return nil, nil, nil
	}
	if len(matches) > MaxPDFObjects {
		return nil, nil, fmt.Errorf("%w: PDF has more than %d objects", ErrTooLarge, MaxPDFObjects)
	}
	images := make([]versionapp.ExtractionImage, 0)
	skipped := make([]pdfSkip, 0)
	for _, match := range matches {
		number := string(data[match[2]:match[3]])
		objectStart := match[1]
		objectEnd := bytes.Index(data[objectStart:], []byte("endobj"))
		if objectEnd < 0 {
			continue
		}
		segment := data[objectStart : objectStart+objectEnd]
		streamIndex := bytes.Index(segment, []byte("stream"))
		if streamIndex < 0 {
			continue
		}
		dict := segment[:streamIndex]
		if !pdfImageSubtype.Match(dict) {
			continue
		}
		source := "pdf:object:" + number
		object, err := parsePDFImageObject(number, dict, segment[streamIndex:])
		if err != nil {
			var tooLarge *limitError
			if errors.As(err, &tooLarge) {
				return nil, nil, fmt.Errorf("%w: %s", ErrTooLarge, tooLarge.reason)
			}
			skipped = append(skipped, pdfSkip{source: source, reason: err.Error()})
			continue
		}
		image, err := decodePDFImageObject(object)
		if err != nil {
			skipped = append(skipped, pdfSkip{source: source, reason: err.Error()})
			continue
		}
		image.Source = source
		images = append(images, image)
	}
	sort.SliceStable(images, func(i, j int) bool { return images[i].Source < images[j].Source })
	return images, skipped, nil
}

// limitError marks a hard limit hit that must fail the whole parse instead of
// becoming a skip.
type limitError struct {
	reason string
}

func (e *limitError) Error() string { return e.reason }

func parsePDFImageObject(number string, dict, streamRegion []byte) (pdfImageObject, error) {
	object := pdfImageObject{
		number: number,
		dict:   dict,
		bits:   8,
		width:  pdfIntValue(dict, pdfWidthPattern),
		height: pdfIntValue(dict, pdfHeightPattern),
	}
	if bits := pdfIntValue(dict, pdfBitsPattern); bits > 0 {
		object.bits = bits
	}
	object.filters = pdfFilterNames(dict)
	streamStart := len("stream")
	if streamStart < len(streamRegion) && streamRegion[streamStart] == '\r' {
		streamStart++
	}
	if streamStart < len(streamRegion) && streamRegion[streamStart] == '\n' {
		streamStart++
	}
	streamEnd := len(streamRegion)
	if lengthMatch := pdfLengthPattern.FindSubmatch(dict); lengthMatch != nil && lengthMatch[2] == nil {
		if length, err := strconv.Atoi(string(lengthMatch[1])); err == nil && length >= 0 && streamStart+length <= len(streamRegion) {
			streamEnd = streamStart + length
		}
	}
	if streamEnd == len(streamRegion) {
		if end := bytes.Index(streamRegion[streamStart:], []byte("endstream")); end >= 0 {
			streamEnd = streamStart + end
		}
	}
	stream := streamRegion[streamStart:streamEnd]
	stream = bytes.TrimSuffix(stream, []byte("\r\n"))
	stream = bytes.TrimSuffix(stream, []byte("\n"))
	if len(stream) == 0 {
		return pdfImageObject{}, fmt.Errorf("image object has an empty stream")
	}
	object.stream = append([]byte(nil), stream...)
	return object, nil
}

func pdfIntValue(dict []byte, pattern *regexp.Regexp) int {
	match := pattern.FindSubmatch(dict)
	if match == nil {
		return 0
	}
	value, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0
	}
	return value
}

func pdfFilterNames(dict []byte) []string {
	match := pdfFilterPattern.FindSubmatch(dict)
	if match == nil {
		return nil
	}
	names := make([]string, 0, 2)
	for _, found := range pdfFilterNamePattern.FindAllSubmatch(match[1], -1) {
		names = append(names, string(found[1]))
	}
	return names
}

// pdfColorComponents returns 1/3/4 for a recognized device color space, 0 when
// it is unknown (component count can be inferred from the pixel data), and -1
// for color spaces that are not decoded (Indexed/Separation palettes).
func pdfColorComponents(dict []byte) int {
	match := pdfColorSpacePattern.FindSubmatch(dict)
	if match == nil {
		return 0
	}
	value := string(match[1])
	switch {
	case strings.Contains(value, "Indexed"), strings.Contains(value, "Separation"),
		strings.Contains(value, "DeviceN"), strings.Contains(value, "Pattern"):
		return -1
	case strings.Contains(value, "DeviceGray"), strings.Contains(value, "CalGray"):
		return 1
	case strings.Contains(value, "DeviceRGB"), strings.Contains(value, "CalRGB"):
		return 3
	case strings.Contains(value, "DeviceCMYK"):
		return 4
	default:
		return 0
	}
}

func decodePDFImageObject(object pdfImageObject) (versionapp.ExtractionImage, error) {
	payload := object.stream
	for index, filter := range object.filters {
		switch filter {
		case "FlateDecode", "Fl":
			decoded, err := inflatePDFStream(payload)
			if err != nil {
				var tooLarge *limitError
				if errors.As(err, &tooLarge) {
					return versionapp.ExtractionImage{}, tooLarge
				}
				return versionapp.ExtractionImage{}, fmt.Errorf("decode PDF image object %s: %v", object.number, err)
			}
			payload = decoded
		case "ASCII85Decode", "A85":
			decoded, err := decodeASCII85PDF(payload)
			if err != nil {
				return versionapp.ExtractionImage{}, fmt.Errorf("decode ASCII85 in PDF image object %s: %v", object.number, err)
			}
			payload = decoded
		case "DCTDecode", "DCT":
			if index != len(object.filters)-1 {
				return versionapp.ExtractionImage{}, fmt.Errorf("unsupported PDF image filter chain ending with DCTDecode")
			}
			mime, width, height, err := ImageDimensions(payload)
			if err != nil || mime != "image/jpeg" {
				return versionapp.ExtractionImage{}, fmt.Errorf("PDF image object %s is not a valid JPEG", object.number)
			}
			return versionapp.ExtractionImage{
				Name:   "pdf-image-" + object.number + ".jpg",
				MIME:   mime,
				Width:  width,
				Height: height,
				Data:   payload,
			}, nil
		default:
			if pdfUnsupportedFilters[filter] {
				return versionapp.ExtractionImage{}, fmt.Errorf("unsupported PDF image filter %s", filter)
			}
			return versionapp.ExtractionImage{}, fmt.Errorf("unsupported PDF image filter %s", filter)
		}
	}
	if object.width <= 0 || object.height <= 0 {
		return versionapp.ExtractionImage{}, fmt.Errorf("PDF image object %s has no usable dimensions", object.number)
	}
	if object.bits != 8 {
		return versionapp.ExtractionImage{}, fmt.Errorf("unsupported PDF image bits per component %d", object.bits)
	}
	components := pdfColorComponents(object.dict)
	if components < 0 {
		return versionapp.ExtractionImage{}, fmt.Errorf("unsupported indexed PDF image color space")
	}
	if components == 0 {
		pixels := object.width * object.height
		switch {
		case pixels > 0 && len(payload)%pixels == 0:
			components = len(payload) / pixels
		default:
			components = 0
		}
	}
	if components != 1 && components != 3 && components != 4 {
		return versionapp.ExtractionImage{}, fmt.Errorf("cannot infer PDF image color space")
	}
	expected := object.width * object.height * components
	if len(payload) < expected {
		return versionapp.ExtractionImage{}, fmt.Errorf("PDF image object %s pixel data is truncated", object.number)
	}
	payload = payload[:expected]
	encoded, err := pdfPixelsToPNG(payload, object.width, object.height, components)
	if err != nil {
		return versionapp.ExtractionImage{}, fmt.Errorf("encode PDF image object %s: %v", object.number, err)
	}
	return versionapp.ExtractionImage{
		Name:   "pdf-image-" + object.number + ".png",
		MIME:   "image/png",
		Width:  object.width,
		Height: object.height,
		Data:   encoded,
	}, nil
}

func pdfPixelsToPNG(payload []byte, width, height, components int) ([]byte, error) {
	var picture image.Image
	switch components {
	case 1:
		picture = &image.Gray{Pix: append([]byte(nil), payload...), Stride: width, Rect: image.Rect(0, 0, width, height)}
	case 3:
		rgba := image.NewRGBA(image.Rect(0, 0, width, height))
		for index := 0; index < width*height; index++ {
			rgba.Pix[index*4] = payload[index*3]
			rgba.Pix[index*4+1] = payload[index*3+1]
			rgba.Pix[index*4+2] = payload[index*3+2]
			rgba.Pix[index*4+3] = 255
		}
		picture = rgba
	case 4:
		rgba := image.NewRGBA(image.Rect(0, 0, width, height))
		for index := 0; index < width*height; index++ {
			cyan := uint32(payload[index*4])
			magenta := uint32(payload[index*4+1])
			yellow := uint32(payload[index*4+2])
			black := uint32(payload[index*4+3])
			rgba.Pix[index*4] = uint8((255 - cyan) * (255 - black) / 255)
			rgba.Pix[index*4+1] = uint8((255 - magenta) * (255 - black) / 255)
			rgba.Pix[index*4+2] = uint8((255 - yellow) * (255 - black) / 255)
			rgba.Pix[index*4+3] = 255
		}
		picture = rgba
	default:
		return nil, fmt.Errorf("unsupported component count %d", components)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func inflatePDFStream(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err == nil {
		defer reader.Close()
		payload, readErr := io.ReadAll(io.LimitReader(reader, version.MaxOOXMLBytes+1))
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(payload)) > version.MaxOOXMLBytes {
			return nil, &limitError{reason: "decompressed PDF image exceeds the limit"}
		}
		return payload, nil
	}
	flateReader := flate.NewReader(bytes.NewReader(data))
	defer flateReader.Close()
	payload, readErr := io.ReadAll(io.LimitReader(flateReader, version.MaxOOXMLBytes+1))
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(payload)) > version.MaxOOXMLBytes {
		return nil, &limitError{reason: "decompressed PDF image exceeds the limit"}
	}
	return payload, nil
}

func decodeASCII85PDF(data []byte) ([]byte, error) {
	if end := bytes.Index(data, []byte("~>")); end >= 0 {
		data = data[:end]
	}
	destination := make([]byte, len(data))
	written, _, err := ascii85.Decode(destination, data, true)
	if err != nil {
		return nil, err
	}
	return destination[:written], nil
}
