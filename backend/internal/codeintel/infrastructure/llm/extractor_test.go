package codeintellm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintellm "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/llm"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

type fakeChat struct {
	content    string
	err        error
	model      string
	modelErr   error
	requests   []llmapp.ChatRequest
	modelCalls int
}

func (f *fakeChat) Chat(_ context.Context, request llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return llmapp.ChatResponse{}, f.err
	}
	return llmapp.ChatResponse{Content: f.content}, nil
}

func (f *fakeChat) ResolveModel(_ context.Context, _ string) (llmapp.ModelInfo, error) {
	f.modelCalls++
	if f.modelErr != nil {
		return llmapp.ModelInfo{}, f.modelErr
	}
	return llmapp.ModelInfo{Model: f.model, ContextWindow: 64000, MaxOutput: 4096}, nil
}

func extractionInput() codeintelapp.FactExtractInput {
	return codeintelapp.FactExtractInput{
		Repo:            codeintelapp.RepoRef{ID: 10, Name: "repo", Path: "/tmp/repo", Branch: "main"},
		CommitSHA:       "1111111111111111111111111111111111111111",
		Path:            "src/routes.ts",
		Lines:           []string{"import { post } from './client';", "export const orders = '/orders';", "export function refund() {}"},
		MaxFacts:        200,
		MaxEdges:        200,
		MaxExcerptLines: 40,
		PromptVersion:   "p5.1",
	}
}

func TestExtractParsesStrictJSONAndBuildsFactKeys(t *testing.T) {
	chat := &fakeChat{content: `{
		"facts": [
			{"kind": "route", "name": "/orders", "signature": "orders", "start_line": 1, "end_line": 2,
			 "confidence": "high", "body": {"route": "/orders"},
			 "evidence": [{"start_line": 1, "end_line": 2}]},
			{"kind": "symbol", "name": "refund", "signature": "refund()", "start_line": 3, "end_line": 3,
			 "confidence": "medium", "evidence": [{"file": "src/routes.ts", "start_line": 3, "end_line": 3}]},
			{"kind": "nonsense", "name": "bad", "confidence": "high"}
		],
		"edges": [
			{"src_kind": "symbol", "src_name": "orders", "dst_kind": "symbol", "dst_name": "refund",
			 "rel": "calls", "confidence": "medium"}
		]
	}`}
	extractor := codeintellm.New(chat)
	output, err := extractor.Extract(context.Background(), extractionInput())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(output.Facts) != 2 {
		t.Fatalf("facts = %+v", output.Facts)
	}
	if output.InvalidFacts != 1 {
		t.Fatalf("invalid facts = %d", output.InvalidFacts)
	}
	route := output.Facts[0]
	if route.Kind != codeintel.EntityRoute || route.Name != "/orders" || route.Path != "src/routes.ts" ||
		route.StartLine != 1 || route.EndLine != 2 || route.Confidence != codeintel.ConfidenceHigh {
		t.Fatalf("route fact = %+v", route)
	}
	if len(route.Evidence) != 1 || route.Evidence[0].StartLine != 1 || route.Evidence[0].EndLine != 2 {
		t.Fatalf("route evidence = %+v", route.Evidence)
	}
	if string(route.Body) != `{"route": "/orders"}` {
		t.Fatalf("route body = %s", route.Body)
	}
	if len(output.Edges) != 1 {
		t.Fatalf("edges = %+v", output.Edges)
	}
	if output.Edges[0].SrcFactKey != "symbol:src/routes.ts#orders" ||
		output.Edges[0].DstFactKey != "symbol:src/routes.ts#refund" ||
		output.Edges[0].Rel != codeintel.EdgeCalls {
		t.Fatalf("edge = %+v", output.Edges[0])
	}
	if len(chat.requests) != 1 {
		t.Fatalf("chat requests = %d", len(chat.requests))
	}
	if len(chat.requests[0].Messages) < 2 {
		t.Fatalf("messages = %+v", chat.requests[0].Messages)
	}
	user := chat.requests[0].Messages[len(chat.requests[0].Messages)-1].Content
	for _, fragment := range []string{"src/routes.ts", "1: import", "3: export function refund", "JSON"} {
		if !strings.Contains(user, fragment) {
			t.Fatalf("prompt missing %q:\n%s", fragment, user)
		}
	}
}

func TestExtractAcceptsFencedJSON(t *testing.T) {
	chat := &fakeChat{content: "```json\n{\"facts\":[],\"edges\":[]}\n```"}
	output, err := codeintellm.New(chat).Extract(context.Background(), extractionInput())
	if err != nil {
		t.Fatalf("Extract fenced: %v", err)
	}
	if len(output.Facts) != 0 || len(output.Edges) != 0 {
		t.Fatalf("output = %+v", output)
	}
}

func TestExtractRejectsInvalidJSONAndGatewayErrors(t *testing.T) {
	if _, err := codeintellm.New(&fakeChat{content: "this is not json"}).Extract(context.Background(), extractionInput()); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := codeintellm.New(&fakeChat{err: errors.New("upstream 500")}).Extract(context.Background(), extractionInput()); err == nil {
		t.Fatal("gateway error swallowed")
	}
	if _, err := codeintellm.New(nil).Extract(context.Background(), extractionInput()); err == nil {
		t.Fatal("nil gateway accepted")
	}
}

func TestExtractorModel(t *testing.T) {
	chat := &fakeChat{model: "deepseek-flash"}
	model, err := codeintellm.New(chat).Model(context.Background())
	if err != nil || model != "deepseek-flash" {
		t.Fatalf("Model = %q, %v", model, err)
	}
	if _, err := codeintellm.New(&fakeChat{modelErr: errors.New("no model")}).Model(context.Background()); err == nil {
		t.Fatal("model error swallowed")
	}
}

func TestImpactParsesCandidates(t *testing.T) {
	chat := &fakeChat{content: `{"candidates": [
		{"subject_kind": "case_version", "subject_id": "42", "reason": "touches the refund flow",
		 "confidence": "medium", "evidence": [{"file": "src/routes.ts", "start_line": 1, "end_line": 2}]},
		{"subject_kind": "nonsense", "subject_id": "1"},
		{"subject_kind": "requirement", "subject_id": "not-a-number"}
	]}`}
	input := codeintelapp.ImpactInferenceInput{
		Repo:          codeintelapp.RepoRef{ID: 10, Name: "repo", Path: "/tmp/repo"},
		CommitSHA:     "1111111111111111111111111111111111111111",
		ChangedFiles:  []codeintel.ChangedFile{{Path: "src/routes.ts", Status: "modified"}},
		Facts:         []codeintel.Fact{{FactKey: "route:/orders", Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders"}},
		Requirements:  []codeintelapp.RequirementRef{{ID: 7, Title: "Refund flow"}},
		PromptVersion: "p5.1",
	}
	output, err := codeintellm.New(chat).Impact(context.Background(), input)
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(output.Candidates) != 1 || output.InvalidCandidates != 2 {
		t.Fatalf("candidates = %+v invalid=%d", output.Candidates, output.InvalidCandidates)
	}
	candidate := output.Candidates[0]
	if candidate.SubjectKind != codeintel.SubjectCaseVersion || candidate.SubjectID != 42 ||
		candidate.Confidence != codeintel.ConfidenceMedium || len(candidate.Evidence) != 1 {
		t.Fatalf("candidate = %+v", candidate)
	}
}
