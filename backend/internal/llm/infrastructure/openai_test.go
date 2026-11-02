package llminfra_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

const testAPIKey = "sk-super-secret"

func openAITestConfig(baseURL string) llm.Config {
	return llm.Config{
		ID:             1,
		Name:           "Test",
		Provider:       llm.ProviderOpenAI,
		Protocol:       llm.ProtocolOpenAI,
		BaseURL:        baseURL,
		APIKey:         testAPIKey,
		Headers:        map[string]string{"X-Org": "team-a"},
		TimeoutSeconds: 60,
		MaxRetries:     2,
		Status:         llm.StatusActive,
	}
}

func TestOpenAIListModels(t *testing.T) {
	var method, path, authorization, org string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		authorization = r.Header.Get("Authorization")
		org = r.Header.Get("X-Org")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"id":"gpt-4o","context_window":128000,"max_output_tokens":16384,"supports_tools":true,"input_modalities":["text","image"]},
			{"id":"gpt-4o-mini"},
			{"id":"   ","context_window":1}
		]}`)
	}))
	defer server.Close()

	cfg := openAITestConfig(server.URL + "/v1/")
	// An inbound Authorization header must never override the configured key.
	cfg.Headers["authorization"] = "Bearer evil"

	client := llminfra.NewClient()
	models, err := client.ListModels(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if method != http.MethodGet || path != "/v1/models" {
		t.Fatalf("request = %s %s", method, path)
	}
	if authorization != "Bearer "+testAPIKey {
		t.Fatalf("authorization = %q", authorization)
	}
	if org != "team-a" {
		t.Fatalf("custom header = %q", org)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "gpt-4o" || models[0].ContextWindow != 128000 || models[0].MaxOutput != 16384 {
		t.Fatalf("first model = %#v", models[0])
	}
	if !models[0].SupportsTools || !models[0].SupportsVision {
		t.Fatalf("first model capabilities = %#v", models[0])
	}
	if models[1].ID != "gpt-4o-mini" || models[1].SupportsTools || models[1].SupportsVision {
		t.Fatalf("second model = %#v", models[1])
	}
}

func TestOpenAIListModelsReportsStatusWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream failed with `+testAPIKey+`"}}`)
	}))
	defer server.Close()

	_, err := llminfra.NewClient().ListModels(context.Background(), openAITestConfig(server.URL))
	if err == nil {
		t.Fatal("ListModels succeeded on HTTP 500")
	}
	message := err.Error()
	if !strings.Contains(message, "HTTP 500") {
		t.Fatalf("error = %q, want status code", message)
	}
	if strings.Contains(message, testAPIKey) {
		t.Fatalf("api key leaked in error %q", message)
	}
}

func TestOpenAITestSucceedsThroughModels(t *testing.T) {
	chatCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-4o"}]}`)
		case "/v1/chat/completions":
			chatCalls++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := llminfra.NewClient().Test(context.Background(), openAITestConfig(server.URL+"/v1"))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK || len(result.Models) != 1 || result.Models[0].ID != "gpt-4o" {
		t.Fatalf("result = %#v", result)
	}
	if result.LatencyMS < 0 {
		t.Fatalf("latency = %d", result.LatencyMS)
	}
	if chatCalls != 0 {
		t.Fatalf("chat probe ran although /models succeeded: %d", chatCalls)
	}
}

func TestOpenAITestFallsBackToChatCompletion(t *testing.T) {
	var chatBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
		case "/v1/chat/completions":
			if r.Method != http.MethodPost {
				t.Errorf("chat method = %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
				t.Errorf("chat authorization = %q", got)
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &chatBody); err != nil {
				t.Errorf("decode chat body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := llminfra.NewClient().Test(context.Background(), openAITestConfig(server.URL+"/v1"))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Models) != 0 {
		t.Fatalf("fallback models = %#v", result.Models)
	}
	if model, _ := chatBody["model"].(string); model == "" {
		t.Fatalf("chat probe body = %#v", chatBody)
	}
	if maxTokens, _ := chatBody["max_tokens"].(float64); maxTokens != 1 {
		t.Fatalf("chat probe max_tokens = %#v", chatBody["max_tokens"])
	}
	if stream, _ := chatBody["stream"].(bool); stream {
		t.Fatalf("chat probe must not stream: %#v", chatBody)
	}
	if messages, _ := chatBody["messages"].([]any); len(messages) == 0 {
		t.Fatalf("chat probe messages = %#v", chatBody["messages"])
	}
}

func TestOpenAITestFailureDoesNotLeakKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, testAPIKey)
	}))
	defer server.Close()

	result, err := llminfra.NewClient().Test(context.Background(), openAITestConfig(server.URL))
	if err == nil {
		t.Fatal("Test succeeded against a failing upstream")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("api key leaked in error %q", err.Error())
	}
	if strings.Contains(result.Error, testAPIKey) {
		t.Fatalf("api key leaked in result %q", result.Error)
	}
	if result.OK {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAIClientHonoursTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	client := llminfra.NewClientWithHTTPClient(&http.Client{Timeout: 40 * time.Millisecond})
	start := time.Now()
	_, err := client.Test(context.Background(), openAITestConfig(server.URL))
	if err == nil {
		t.Fatal("Test did not time out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("api key leaked in error %q", err.Error())
	}
}

func TestOpenAIRejectsInvalidBaseURL(t *testing.T) {
	_, err := llminfra.NewClient().ListModels(context.Background(), openAITestConfig("not-a-url"))
	if err == nil {
		t.Fatal("invalid base URL accepted")
	}
}
