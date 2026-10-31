package agentapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

func TestLLMSummarizerBuildsPromptAndTrimsOutput(t *testing.T) {
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{Content: "  用户要登录功能，未决：登录方式。  "}}}
	summarizer := agentapp.NewLLMSummarizer(chat)

	summary, err := summarizer.Summarize(context.Background(), []agentapp.Message{
		{ID: 1, Role: "user", Content: "我要一个登录功能"},
		{ID: 2, Role: "assistant", Content: "请问登录方式？"},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary != "用户要登录功能，未决：登录方式。" {
		t.Fatalf("summary = %q", summary)
	}
	request := chat.request(0)
	if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
		t.Fatalf("messages = %#v", request.Messages)
	}
	if !strings.Contains(request.Messages[1].Content, "[user] 我要一个登录功能") {
		t.Fatalf("transcript = %q", request.Messages[1].Content)
	}
	if request.MaxOutputTokens <= 0 {
		t.Fatalf("max output tokens = %d", request.MaxOutputTokens)
	}
}

func TestLLMSummarizerReturnsEmptyForNoMessagesWithoutCallingChat(t *testing.T) {
	chat := &scriptedChat{}
	summary, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary != "" {
		t.Fatalf("summary = %q", summary)
	}
	if chat.requestCount() != 0 {
		t.Fatalf("chat calls = %d", chat.requestCount())
	}
}

func TestLLMSummarizerTruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("字", 3000)
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{Content: long}}}
	summary, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if runes := []rune(summary); len(runes) > 2000 {
		t.Fatalf("summary length = %d, want <= 2000", len(runes))
	}
}

func TestLLMSummarizerPropagatesGatewayFailure(t *testing.T) {
	chat := &scriptedChat{errors: []error{errors.New("model down")}}
	_, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLLMSummarizerWithoutGatewayFails(t *testing.T) {
	_, err := agentapp.NewLLMSummarizer(nil).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}
