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
	"strconv"
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

// Available reports whether a vision-capable default model is configured and,
// when it is not, the typed reason (M-07). A deterministic configuration or
// capability failure is returned as data (Retryable=false) so the attachment
// layer can build the 400 vision_required diagnostics; a transport or
// unclassified failure is gateway_unreachable with Retryable=true so the API
// answers 503 ai_unavailable instead of blaming the configuration.
func (c *VisionCaptioner) Available(ctx context.Context) (versionapp.ImageAvailability, error) {
	if c == nil || c.gateway == nil {
		return versionapp.ImageAvailability{ReasonCode: versionapp.VisionReasonNoDefaultConfig}, nil
	}
	model, err := c.gateway.ResolveModel(ctx, 0, "", llmapp.ModelRequirements{Vision: true})
	if err == nil {
		availability := versionapp.ImageAvailability{Available: true, ModelName: model.Model}
		if model.ConfigID > 0 {
			availability.ConfigID = strconv.FormatInt(model.ConfigID, 10)
		}
		if model.ModelID > 0 {
			availability.ModelID = strconv.FormatInt(model.ModelID, 10)
		}
		return availability, nil
	}
	return classifyVisionResolveError(err), nil
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

// classifyVisionResolveError maps a ResolveModel failure onto the stable
// availability reason codes (M-07). Task 7 carries config_id/config_name/
// default_model_id on config/capability errors, so the diagnosis can name the
// broken configuration. The underlying LLM code is preserved in CauseCode.
func classifyVisionResolveError(err error) versionapp.ImageAvailability {
	availability := versionapp.ImageAvailability{
		ReasonCode: versionapp.VisionReasonGatewayUnreachable,
		Retryable:  true,
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		return availability
	}
	availability.CauseCode = apiErr.Code
	availability.CauseMessage = apiErr.Message
	availability.ConfigID = detailString(apiErr.Details, "config_id")
	availability.ConfigName = detailString(apiErr.Details, "config_name")
	availability.ModelID = detailString(apiErr.Details, "default_model_id")
	availability.ModelName = detailString(apiErr.Details, "model")
	availability.Missing = detailStrings(apiErr.Details, "missing_capabilities")

	switch apiErr.Code {
	case llmapp.CodeLLMConfigInvalid:
		availability.Retryable = false
		message := strings.ToLower(apiErr.Message)
		switch {
		case strings.Contains(message, "deprecated or disabled"):
			availability.ReasonCode = versionapp.VisionReasonDefaultModelDisabled
		case strings.Contains(message, "has no default model"),
			strings.Contains(message, "is not in the catalog"):
			availability.ReasonCode = versionapp.VisionReasonDefaultModelMissing
		default:
			// Another deterministic configuration error (for example the
			// thinking_type=disabled + reasoning_effort contradiction). Claiming
			// the default model is missing would send the operator to fix the
			// wrong setting, so keep the generic deterministic reason and carry
			// the real cause in CauseCode/CauseMessage.
			availability.ReasonCode = versionapp.VisionReasonDefaultModelInvalid
		}
	case llmapp.CodeLLMCapabilityMismatch:
		availability.Retryable = false
		if availability.Missing == nil {
			availability.Missing = []string{}
		}
		if capabilityMissing(availability.Missing, "vision") ||
			strings.Contains(strings.ToLower(apiErr.Message), "does not support vision") {
			availability.ReasonCode = versionapp.VisionReasonDefaultModelNotVision
			if !capabilityMissing(availability.Missing, "vision") {
				availability.Missing = append(availability.Missing, "vision")
			}
		} else {
			// A capability gap for a non-vision requirement (for example
			// reasoning implied by the thinking settings) is not "not vision":
			// name it in missing_capabilities and keep the cause-specific next
			// action.
			availability.ReasonCode = versionapp.VisionReasonDefaultModelInvalid
		}
		if availability.ModelName == "" {
			availability.ModelName = availability.ModelID
		}
	case llmapp.CodeAIUnavailable:
		// ai_unavailable is also the code for "no default config"/"default
		// config disabled", which are deterministic configuration outcomes.
		// Only those exact deployment messages are non-retryable; anything else
		// stays gateway_unreachable and is retried.
		if deterministicVisionConfigMessage(apiErr.Message) {
			availability.ReasonCode = versionapp.VisionReasonNoDefaultConfig
			availability.Retryable = false
		}
	}
	return availability
}

// capabilityMissing reports whether the resolved model rejected a specific
// required capability. The comparison is case-insensitive because the LLM
// application lowercases capability names while a future caller may not.
func capabilityMissing(missing []string, capability string) bool {
	for _, value := range missing {
		if strings.EqualFold(strings.TrimSpace(value), capability) {
			return true
		}
	}
	return false
}

// deterministicVisionConfigMessage identifies the ai_unavailable messages that
// describe a configuration problem rather than a transient gateway failure.
// They come from llm.Service.selectConfig/resolveChatTarget, so the wording is
// stable.
func deterministicVisionConfigMessage(message string) bool {
	lower := strings.ToLower(message)
	for _, fragment := range []string{
		"no default llm config",
		"default llm config is disabled",
		"llm chat gateway is not configured",
	} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func detailString(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	switch value := details[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	default:
		return ""
	}
}

func detailStrings(details map[string]any, key string) []string {
	if details == nil {
		return nil
	}
	switch value := details[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}
