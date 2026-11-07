// VisionCaptioner adapts the existing LLM chat gateway to the version
// context's per-image vision port. The version application only knows the
// VisionCaptioner port; this infrastructure implementation resolves the
// default LLM catalog model with a vision requirement and sends the image
// bytes through the existing chat gateway as llm.ChatImage (spec §4.25
// R-E2-1, §4.26 R-E3-4).
package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VisionGateway is the narrow LLM application surface the captioner needs. It
// is satisfied by *llmapp.Service and mocked in tests; keeping it narrow means
// the adapter cannot accidentally use admin-only configuration methods.
type VisionGateway interface {
	// ResolveModel selects the default model and enforces the vision
	// capability requirement (R-E2-2).
	ResolveModel(ctx context.Context, cfgID int64, modelID string, required ...llmapp.ModelRequirements) (llmapp.ModelInfo, error)
	// Chat performs one completion through the existing LLM gateway.
	Chat(ctx context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error)
}

// imageCaptionPrompt asks for one concise, factual caption. The caption is
// stored as an attachment_image_caption fact and shown in the UI/task packs, so
// it must not be a free-form conversation or a guess.
const imageCaptionPrompt = "Describe this image for a software requirements review. " +
	"Return one concise factual caption (1-3 sentences) covering the visible screen, diagram or document content, " +
	"including labels, fields, values and any text needed to understand it. " +
	"Do not speculate about information that is not visible."

// VisionCaptioner implements versionapp.VisionCaptioner over the LLM chat
// gateway. A missing default model, a deprecated/disabled model or a model
// without supports_vision is reported as versionapp.ErrVisionUnavailable
// instead of a transient failure.
type VisionCaptioner struct {
	gateway VisionGateway
}

// NewVisionCaptioner builds the adapter. The composition root passes the LLM
// application service; a nil gateway makes every call report unavailable.
func NewVisionCaptioner(gateway VisionGateway) *VisionCaptioner {
	return &VisionCaptioner{gateway: gateway}
}

var _ versionapp.VisionCaptioner = (*VisionCaptioner)(nil)

// Available reports whether a vision-capable default model is configured.
func (c *VisionCaptioner) Available(ctx context.Context) (bool, error) {
	if c == nil || c.gateway == nil {
		return false, nil
	}
	_, err := c.gateway.ResolveModel(ctx, 0, "", llmapp.ModelRequirements{Vision: true})
	if err == nil {
		return true, nil
	}
	if visionModelUnavailable(err) {
		return false, nil
	}
	return false, err
}

// Caption analyzes one image and returns its caption. Empty captions are
// failures: storing an empty caption would look like a successful analysis.
func (c *VisionCaptioner) Caption(ctx context.Context, mime string, data []byte) (versionapp.ImageCaption, error) {
	if c == nil || c.gateway == nil {
		return versionapp.ImageCaption{}, fmt.Errorf("%w: vision captioner is not configured", versionapp.ErrVisionUnavailable)
	}
	model, err := c.gateway.ResolveModel(ctx, 0, "", llmapp.ModelRequirements{Vision: true})
	if err != nil {
		if visionModelUnavailable(err) {
			return versionapp.ImageCaption{}, fmt.Errorf("%w: %v", versionapp.ErrVisionUnavailable, err)
		}
		return versionapp.ImageCaption{}, err
	}
	response, err := c.gateway.Chat(ctx, llmapp.ChatRequest{
		Model: model.Model,
		Messages: []llmapp.ChatMessage{{
			Role:    "user",
			Content: imageCaptionPrompt,
			Images:  []llmapp.ChatImage{{MIME: mime, Data: data}},
		}},
	})
	if err != nil {
		return versionapp.ImageCaption{}, err
	}
	caption := strings.TrimSpace(response.Content)
	if caption == "" {
		return versionapp.ImageCaption{}, errors.New("vision model returned an empty caption")
	}
	return versionapp.ImageCaption{Caption: caption, Model: model.Model}, nil
}

// visionModelUnavailable classifies a resolution failure as "no usable vision
// model" (stable configuration outcome) versus a transient infrastructure
// failure that callers must retry.
func visionModelUnavailable(err error) bool {
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case llmapp.CodeLLMConfigInvalid, llmapp.CodeLLMCapabilityMismatch, llmapp.CodeAIUnavailable:
		return true
	default:
		return false
	}
}
