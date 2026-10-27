package httpapi

import _ "embed"

// pixelPNG is the 1x1 PNG attached by an image connectivity test. It is
// embedded in the binary so the endpoint works from any working directory,
// including the Docker runtime image that ships only the binary and the
// OpenAPI document.
//
//go:embed testdata/pixel.png
var pixelPNG []byte
