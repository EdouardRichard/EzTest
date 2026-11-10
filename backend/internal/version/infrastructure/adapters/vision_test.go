package adapters

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llmdomain "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeVisionGateway struct {
	resolveModel llmapp.ModelInfo
	resolveErr   error
	chatResponse llmapp.ChatResponse
	chatErr      error
	lastRequest  llmapp.ChatRequest
	resolveCalls int
	chatCalls    int
	lastRequired []llmapp.ModelRequirements
}

func (f *fakeVisionGateway) ResolveModel(_ context.Context, _ int64, _ string, required ...llmapp.ModelRequirements) (llmapp.ModelInfo, error) {
	f.resolveCalls++
	f.lastRequired = required
	if f.resolveErr != nil {
		return llmapp.ModelInfo{}, f.resolveErr
	}
	return f.resolveModel, nil
}

func (f *fakeVisionGateway) Chat(_ context.Context, request llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	f.chatCalls++
	f.lastRequest = request
	if f.chatErr != nil {
		return llmapp.ChatResponse{}, f.chatErr
	}
	return f.chatResponse, nil
}

func TestVisionCaptionerCaptionsImage(t *testing.T) {
	gateway := &fakeVisionGateway{
		resolveModel: llmapp.ModelInfo{Model: "vision-llm"},
		chatResponse: llmapp.ChatResponse{Content: "  登录界面截图：手机号与验证码输入框  "},
	}
	captioner := NewVisionCaptioner(gateway)

	availability, err := captioner.Available(context.Background())
	if err != nil || !availability.Available {
		t.Fatalf("Available = %#v, %v", availability, err)
	}
	caption, err := captioner.Caption(context.Background(), "image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("Caption: %v", err)
	}
	if caption.Caption != "登录界面截图：手机号与验证码输入框" || caption.Model != "vision-llm" {
		t.Fatalf("caption = %#v", caption)
	}
	if gateway.lastRequest.Model != "vision-llm" {
		t.Fatalf("chat model = %q", gateway.lastRequest.Model)
	}
	if len(gateway.lastRequest.Messages) != 1 {
		t.Fatalf("messages = %#v", gateway.lastRequest.Messages)
	}
	message := gateway.lastRequest.Messages[0]
	if message.Role != "user" || len(message.Images) != 1 ||
		message.Images[0].MIME != "image/png" || string(message.Images[0].Data) != "png-bytes" {
		t.Fatalf("message = %#v", message)
	}
}

// TestVisionCaptionerAvailableReportsTypedReasons pins the M-07 diagnosis
// contract: the availability probe maps every deterministic LLM configuration
// failure to a stable reason_code (with the config/model identity carried by
// Task 7's resolve details) and classifies transport/unclassified failures as
// retryable gateway_unreachable instead of collapsing everything into "no
// vision model".
func TestVisionCaptionerAvailableReportsTypedReasons(t *testing.T) {
	tests := []struct {
		name          string
		resolveErr    error
		wantReason    string
		wantRetryable bool
		wantCause     string
		wantCauseText bool
		wantConfigID  string
		wantConfig    string
		wantModel     string
		wantMissing   []string
	}{
		{
			name: "no default config",
			resolveErr: &platform.Error{
				Status: http.StatusServiceUnavailable, Code: llmapp.CodeAIUnavailable,
				Message: "no default llm config is configured", Retryable: true,
			},
			wantReason: versionapp.VisionReasonNoDefaultConfig,
			wantCause:  llmapp.CodeAIUnavailable,
		},
		{
			name: "default llm config disabled",
			resolveErr: &platform.Error{
				Status: http.StatusServiceUnavailable, Code: llmapp.CodeAIUnavailable,
				Message: "default llm config is disabled", Retryable: true,
			},
			wantReason: versionapp.VisionReasonNoDefaultConfig,
			wantCause:  llmapp.CodeAIUnavailable,
		},
		{
			name: "default model missing",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMConfigInvalid,
				Message: "llm config has no default model; set default_model_id before running agents",
				Details: map[string]any{
					"field": "default_model_id", "config_id": "7", "config_name": "主配置", "default_model_id": "",
				},
			},
			wantReason:   versionapp.VisionReasonDefaultModelMissing,
			wantCause:    llmapp.CodeLLMConfigInvalid,
			wantConfigID: "7",
			wantConfig:   "主配置",
		},
		{
			name: "default model deleted from catalog",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMConfigInvalid,
				Message: `default model 9 is not in the catalog of llm config "主配置"`,
				Details: map[string]any{"config_id": "7", "config_name": "主配置", "default_model_id": "9"},
			},
			wantReason:   versionapp.VisionReasonDefaultModelMissing,
			wantCause:    llmapp.CodeLLMConfigInvalid,
			wantConfigID: "7",
			wantConfig:   "主配置",
		},
		{
			name: "default model disabled",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMConfigInvalid,
				Message: `default model "gpt-4" is deprecated or disabled; set default_model_id to an active model`,
				Details: map[string]any{"config_id": "7", "config_name": "主配置", "default_model_id": "9"},
			},
			wantReason:   versionapp.VisionReasonDefaultModelDisabled,
			wantCause:    llmapp.CodeLLMConfigInvalid,
			wantConfigID: "7",
			wantConfig:   "主配置",
		},
		{
			name: "default model without vision",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMCapabilityMismatch,
				Message: `llm model "gpt-4" does not support vision required by this role`,
				Details: map[string]any{
					"model": "gpt-4", "missing_capabilities": []string{"vision"},
					"config_id": "7", "config_name": "主配置", "default_model_id": "9",
				},
			},
			wantReason:   versionapp.VisionReasonDefaultModelNotVision,
			wantCause:    llmapp.CodeLLMCapabilityMismatch,
			wantConfigID: "7",
			wantConfig:   "主配置",
			wantModel:    "gpt-4",
			wantMissing:  []string{"vision"},
		},
		{
			name: "default model without a non-vision capability",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMCapabilityMismatch,
				Message: `llm model "gpt-4" does not support reasoning required by this role`,
				Details: map[string]any{
					"model": "gpt-4", "missing_capabilities": []string{"reasoning"},
					"config_id": "7", "config_name": "主配置", "default_model_id": "9",
				},
			},
			wantReason:    versionapp.VisionReasonDefaultModelInvalid,
			wantCause:     llmapp.CodeLLMCapabilityMismatch,
			wantCauseText: true,
			wantConfigID:  "7",
			wantConfig:    "主配置",
			wantModel:     "gpt-4",
			wantMissing:   []string{"reasoning"},
		},
		{
			name: "capability mismatch reported by the message only",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMCapabilityMismatch,
				Message: `llm model "gpt-4" does not support vision required by this role`,
				Details: map[string]any{
					"model": "gpt-4", "config_id": "7", "config_name": "主配置",
				},
			},
			wantReason:   versionapp.VisionReasonDefaultModelNotVision,
			wantCause:    llmapp.CodeLLMCapabilityMismatch,
			wantConfigID: "7",
			wantConfig:   "主配置",
			wantModel:    "gpt-4",
			wantMissing:  []string{"vision"},
		},
		{
			name: "unrelated configuration error",
			resolveErr: &platform.Error{
				Status: http.StatusBadRequest, Code: llmapp.CodeLLMConfigInvalid,
				Message: `thinking_type=disabled cannot be combined with reasoning_effort="high"; set reasoning_effort=none or enable thinking`,
				Details: map[string]any{
					"thinking_type": "disabled", "reasoning_effort": "high",
					"config_id": "7", "config_name": "主配置", "default_model_id": "9",
				},
			},
			wantReason:    versionapp.VisionReasonDefaultModelInvalid,
			wantCause:     llmapp.CodeLLMConfigInvalid,
			wantCauseText: true,
			wantConfigID:  "7",
			wantConfig:    "主配置",
		},
		{
			name: "gateway transport failure",
			resolveErr: &platform.Error{
				Status: http.StatusServiceUnavailable, Code: llmapp.CodeAIUnavailable,
				Message: "llm request failed: dial tcp: connection refused", Retryable: true,
			},
			wantReason:    versionapp.VisionReasonGatewayUnreachable,
			wantRetryable: true,
			wantCause:     llmapp.CodeAIUnavailable,
		},
		{
			name:          "unclassified failure",
			resolveErr:    errors.New("database is down"),
			wantReason:    versionapp.VisionReasonGatewayUnreachable,
			wantRetryable: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			captioner := NewVisionCaptioner(&fakeVisionGateway{resolveErr: tc.resolveErr})
			availability, err := captioner.Available(context.Background())
			if err != nil {
				t.Fatalf("Available returned an error: %v", err)
			}
			if availability.Available {
				t.Fatalf("availability = %#v, want unavailable", availability)
			}
			if availability.ReasonCode != tc.wantReason {
				t.Fatalf("reason = %q, want %q", availability.ReasonCode, tc.wantReason)
			}
			if availability.Retryable != tc.wantRetryable {
				t.Fatalf("retryable = %v, want %v", availability.Retryable, tc.wantRetryable)
			}
			if availability.CauseCode != tc.wantCause {
				t.Fatalf("cause code = %q, want %q", availability.CauseCode, tc.wantCause)
			}
			if tc.wantCauseText && strings.TrimSpace(availability.CauseMessage) == "" {
				t.Fatalf("cause message = %q, want the real LLM message", availability.CauseMessage)
			}
			if availability.ConfigID != tc.wantConfigID || availability.ConfigName != tc.wantConfig {
				t.Fatalf("config = %q/%q, want %q/%q",
					availability.ConfigID, availability.ConfigName, tc.wantConfigID, tc.wantConfig)
			}
			if availability.ModelName != tc.wantModel {
				t.Fatalf("model = %q, want %q", availability.ModelName, tc.wantModel)
			}
			if strings.Join(availability.Missing, ",") != strings.Join(tc.wantMissing, ",") {
				t.Fatalf("missing = %#v, want %#v", availability.Missing, tc.wantMissing)
			}
		})
	}
}

func TestVisionCaptionerCaptionRejectsMissingVisionModel(t *testing.T) {
	gateway := &fakeVisionGateway{resolveErr: &platform.Error{
		Status:  http.StatusBadRequest,
		Code:    llmapp.CodeLLMConfigInvalid,
		Message: "llm config has no default model",
	}}
	captioner := NewVisionCaptioner(gateway)
	if _, err := captioner.Caption(context.Background(), "image/png", []byte("png")); !errors.Is(err, versionapp.ErrVisionUnavailable) {
		t.Fatalf("Caption err = %v, want ErrVisionUnavailable", err)
	}
	if gateway.chatCalls != 0 {
		t.Fatal("caption attempted a chat without a vision model")
	}
}

func TestVisionCaptionerPropagatesUpstreamFailure(t *testing.T) {
	gateway := &fakeVisionGateway{
		resolveModel: llmapp.ModelInfo{Model: "vision-llm"},
		chatErr:      errors.New("upstream vision 500"),
	}
	captioner := NewVisionCaptioner(gateway)
	if _, err := captioner.Caption(context.Background(), "image/png", []byte("png")); err == nil ||
		!strings.Contains(err.Error(), "upstream vision 500") {
		t.Fatalf("Caption err = %v", err)
	}
}

func TestVisionCaptionerRejectsEmptyCaption(t *testing.T) {
	gateway := &fakeVisionGateway{
		resolveModel: llmapp.ModelInfo{Model: "vision-llm"},
		chatResponse: llmapp.ChatResponse{Content: "   "},
	}
	captioner := NewVisionCaptioner(gateway)
	if _, err := captioner.Caption(context.Background(), "image/png", []byte("png")); err == nil {
		t.Fatal("empty caption accepted")
	}
}

// fakeVisionLLMRepo is the minimal LLM repository behind a real
// llmapp.Service in the coupling test below. Only the read paths used by
// ResolveModel are implemented; a mutation means the test wired the wrong path.
type fakeVisionLLMRepo struct {
	configs []llmdomain.Config
	models  map[int64][]llmdomain.Model
}

func (f *fakeVisionLLMRepo) ListConfigs(context.Context) ([]llmdomain.Config, error) {
	return f.configs, nil
}

func (f *fakeVisionLLMRepo) GetConfig(_ context.Context, id int64) (llmdomain.Config, error) {
	for _, cfg := range f.configs {
		if cfg.ID == id {
			return cfg, nil
		}
	}
	return llmdomain.Config{}, llmdomain.ErrConfigNotFound
}

func (f *fakeVisionLLMRepo) CreateConfig(context.Context, llmdomain.Config) (llmdomain.Config, error) {
	return llmdomain.Config{}, errors.New("unexpected CreateConfig")
}

func (f *fakeVisionLLMRepo) UpdateConfig(context.Context, llmdomain.Config) (llmdomain.Config, error) {
	return llmdomain.Config{}, errors.New("unexpected UpdateConfig")
}

func (f *fakeVisionLLMRepo) DeleteConfig(context.Context, int64) error {
	return errors.New("unexpected DeleteConfig")
}

func (f *fakeVisionLLMRepo) ListModels(context.Context) ([]llmdomain.Model, error) {
	models := make([]llmdomain.Model, 0)
	for _, configModels := range f.models {
		models = append(models, configModels...)
	}
	return models, nil
}

func (f *fakeVisionLLMRepo) ListModelsByConfig(_ context.Context, configID int64) ([]llmdomain.Model, error) {
	return f.models[configID], nil
}

func (f *fakeVisionLLMRepo) UpsertModels(context.Context, int64, []llmdomain.Model, time.Time) ([]llmdomain.Model, error) {
	return nil, errors.New("unexpected UpsertModels")
}

func (f *fakeVisionLLMRepo) InsertModel(context.Context, llmdomain.Model) (llmdomain.Model, error) {
	return llmdomain.Model{}, errors.New("unexpected InsertModel")
}

func (f *fakeVisionLLMRepo) UpdateModel(context.Context, llmdomain.Model) (llmdomain.Model, error) {
	return llmdomain.Model{}, errors.New("unexpected UpdateModel")
}

func (f *fakeVisionLLMRepo) DeleteModel(context.Context, int64) error {
	return errors.New("unexpected DeleteModel")
}

type fakeVisionChatTransport struct{}

func (*fakeVisionChatTransport) Chat(context.Context, llmdomain.Config, llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	return llmapp.ChatResponse{}, errors.New("unexpected Chat")
}

// TestVisionCaptionerClassifiesRealLLMResolveErrors is the coupling test for
// the message-based classifier: every error is produced by the real
// llmapp.Service.ResolveModel path, so a wording change in llm/application breaks
// this test instead of silently degrading the M-07 diagnosis.
func TestVisionCaptionerClassifiesRealLLMResolveErrors(t *testing.T) {
	activeConfig := func(defaultModelID int64) llmdomain.Config {
		return llmdomain.Config{
			ID: 7, Name: "主配置", Status: llmdomain.StatusActive,
			IsDefault: true, DefaultModelID: defaultModelID,
			ThinkingType: llmdomain.ThinkingTypeAuto,
		}
	}
	visionModel := llmdomain.Model{ID: 11, ConfigID: 7, Model: "vision-llm", SupportsVision: true}
	plainModel := llmdomain.Model{ID: 11, ConfigID: 7, Model: "plain-llm"}

	tests := []struct {
		name          string
		repo          *fakeVisionLLMRepo
		withChat      bool
		wantReason    string
		wantCause     string
		wantRetryable bool
		wantMissing   []string
	}{
		{
			name:       "no default config",
			repo:       &fakeVisionLLMRepo{},
			withChat:   true,
			wantReason: versionapp.VisionReasonNoDefaultConfig,
			wantCause:  llmapp.CodeAIUnavailable,
		},
		{
			name: "default config disabled",
			repo: &fakeVisionLLMRepo{configs: []llmdomain.Config{func() llmdomain.Config {
				cfg := activeConfig(11)
				cfg.Status = llmdomain.StatusDisabled
				return cfg
			}()}, models: map[int64][]llmdomain.Model{7: {visionModel}}},
			withChat:   true,
			wantReason: versionapp.VisionReasonNoDefaultConfig,
			wantCause:  llmapp.CodeAIUnavailable,
		},
		{
			name: "no default model",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{activeConfig(0)},
				models:  map[int64][]llmdomain.Model{7: {visionModel}},
			},
			withChat:    true,
			wantReason:  versionapp.VisionReasonDefaultModelMissing,
			wantCause:   llmapp.CodeLLMConfigInvalid,
			wantMissing: nil,
		},
		{
			name: "default model not in the catalog",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{activeConfig(99)},
				models:  map[int64][]llmdomain.Model{7: {visionModel}},
			},
			withChat:   true,
			wantReason: versionapp.VisionReasonDefaultModelMissing,
			wantCause:  llmapp.CodeLLMConfigInvalid,
		},
		{
			name: "default model deprecated",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{activeConfig(11)},
				models:  map[int64][]llmdomain.Model{7: {func() llmdomain.Model { m := visionModel; m.Deprecated = true; return m }()}},
			},
			withChat:   true,
			wantReason: versionapp.VisionReasonDefaultModelDisabled,
			wantCause:  llmapp.CodeLLMConfigInvalid,
		},
		{
			name: "thinking contradiction",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{func() llmdomain.Config {
					cfg := activeConfig(11)
					cfg.ThinkingType = llmdomain.ThinkingTypeDisabled
					cfg.ReasoningEffort = llmdomain.ReasoningEffortHigh
					return cfg
				}()},
				models: map[int64][]llmdomain.Model{7: {visionModel}},
			},
			withChat:   true,
			wantReason: versionapp.VisionReasonDefaultModelInvalid,
			wantCause:  llmapp.CodeLLMConfigInvalid,
		},
		{
			name: "default model without vision",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{activeConfig(11)},
				models:  map[int64][]llmdomain.Model{7: {plainModel}},
			},
			withChat:    true,
			wantReason:  versionapp.VisionReasonDefaultModelNotVision,
			wantCause:   llmapp.CodeLLMCapabilityMismatch,
			wantMissing: []string{"vision"},
		},
		{
			name: "default model without reasoning",
			repo: &fakeVisionLLMRepo{
				configs: []llmdomain.Config{func() llmdomain.Config {
					cfg := activeConfig(11)
					cfg.ThinkingType = llmdomain.ThinkingTypeEnabled
					return cfg
				}()},
				models: map[int64][]llmdomain.Model{7: {visionModel}},
			},
			withChat:    true,
			wantReason:  versionapp.VisionReasonDefaultModelInvalid,
			wantCause:   llmapp.CodeLLMCapabilityMismatch,
			wantMissing: []string{"reasoning"},
		},
		{
			name:       "chat gateway not configured",
			repo:       &fakeVisionLLMRepo{configs: []llmdomain.Config{activeConfig(11)}},
			wantReason: versionapp.VisionReasonNoDefaultConfig,
			wantCause:  llmapp.CodeAIUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := llmapp.New(tc.repo, nil, nil, nil, nil, nil)
			if tc.withChat {
				service = llmapp.New(tc.repo, nil, nil, nil, nil, nil, llmapp.WithChatClient(&fakeVisionChatTransport{}))
			}
			availability, err := NewVisionCaptioner(service).Available(context.Background())
			if err != nil {
				t.Fatalf("Available returned an error: %v", err)
			}
			if availability.Available {
				t.Fatalf("availability = %#v, want unavailable", availability)
			}
			if availability.ReasonCode != tc.wantReason {
				t.Fatalf("reason = %q, want %q (cause %q message %q)",
					availability.ReasonCode, tc.wantReason, availability.CauseCode, availability.CauseMessage)
			}
			if availability.CauseCode != tc.wantCause {
				t.Fatalf("cause code = %q, want %q", availability.CauseCode, tc.wantCause)
			}
			if availability.Retryable != tc.wantRetryable {
				t.Fatalf("retryable = %v, want %v", availability.Retryable, tc.wantRetryable)
			}
			if strings.Join(availability.Missing, ",") != strings.Join(tc.wantMissing, ",") {
				t.Fatalf("missing = %#v, want %#v", availability.Missing, tc.wantMissing)
			}
			if tc.wantReason == versionapp.VisionReasonDefaultModelInvalid {
				if strings.TrimSpace(availability.CauseMessage) == "" {
					t.Fatal("generic deterministic reason lost the real LLM message")
				}
				if availability.ConfigID != "7" || availability.ConfigName != "主配置" {
					t.Fatalf("config identity = %q/%q", availability.ConfigID, availability.ConfigName)
				}
			}
		})
	}
}
