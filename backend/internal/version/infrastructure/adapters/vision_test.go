package adapters

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
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

	available, err := captioner.Available(context.Background())
	if err != nil || !available {
		t.Fatalf("Available = %v, %v", available, err)
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

func TestVisionCaptionerReportsUnavailableWithoutVisionModel(t *testing.T) {
	gateway := &fakeVisionGateway{resolveErr: &platform.Error{
		Status:  http.StatusBadRequest,
		Code:    llmapp.CodeLLMConfigInvalid,
		Message: "llm config has no default model",
	}}
	captioner := NewVisionCaptioner(gateway)
	available, err := captioner.Available(context.Background())
	if err != nil || available {
		t.Fatalf("Available = %v, %v; want false, nil", available, err)
	}
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
