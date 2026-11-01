package knowledgeadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeKnowledgeService implements the adapter's narrow knowledge surface.
type fakeKnowledgeService struct {
	injectFn func(ctx context.Context, input knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error)
	usageFn  func(ctx context.Context, agentRunID, workItemID int64, items []knowledgeapp.Injected) error
	createFn func(ctx context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error)

	injectCalls int
	injectInput knowledgeapp.InjectInput
	usageCalls  int
	usageRunID  int64
	usageItemID int64
	usageItems  []knowledgeapp.Injected
	createCalls int
	createActor platform.Actor
	createInput knowledgeapp.CreateInput
}

func (f *fakeKnowledgeService) Inject(ctx context.Context, input knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error) {
	f.injectCalls++
	f.injectInput = input
	if f.injectFn != nil {
		return f.injectFn(ctx, input)
	}
	return nil, nil
}

func (f *fakeKnowledgeService) RecordUsage(ctx context.Context, agentRunID, workItemID int64, items []knowledgeapp.Injected) error {
	f.usageCalls++
	f.usageRunID = agentRunID
	f.usageItemID = workItemID
	f.usageItems = items
	if f.usageFn != nil {
		return f.usageFn(ctx, agentRunID, workItemID, items)
	}
	return nil
}

func (f *fakeKnowledgeService) Create(ctx context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error) {
	f.createCalls++
	f.createActor = actor
	f.createInput = input
	if f.createFn != nil {
		return f.createFn(ctx, actor, input)
	}
	return knowledge.Item{}, errors.New("create not configured")
}

// fakeVersionSystemReader implements VersionSystemReader.
type fakeVersionSystemReader struct {
	calls     int
	versionID int64
	keys      []string
	err       error
}

func (f *fakeVersionSystemReader) VersionSystemKeys(_ context.Context, versionID int64) ([]string, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.keys...), nil
}

func TestRetrieveForVersionDerivesSystemKeysFromVersionFamilies(t *testing.T) {
	service := &fakeKnowledgeService{injectFn: func(context.Context, knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error) {
		return []knowledgeapp.Injected{
			{ID: 7001, Title: "支付回调重试约定", Snippet: "回调必须幂等", Scope: knowledge.ScopeGlobal, Tokens: 12},
			{ID: 7002, Title: "统一登录", Snippet: "复用 SSO", Scope: knowledge.ScopeSystem, Tokens: 30},
		}, nil
	}}
	systems := &fakeVersionSystemReader{keys: []string{"payment", "auth"}}
	adapter := New(service, systems)

	refs, tokens, err := adapter.RetrieveForVersion(context.Background(), 10, nil)
	if err != nil {
		t.Fatalf("RetrieveForVersion: %v", err)
	}
	if systems.calls != 1 || systems.versionID != 10 {
		t.Fatalf("version reader calls = %d version = %d", systems.calls, systems.versionID)
	}
	if service.injectInput.VersionID != 10 {
		t.Fatalf("inject version = %d", service.injectInput.VersionID)
	}
	if strings.Join(service.injectInput.SystemKeys, ",") != "payment,auth" {
		t.Fatalf("inject system keys = %v", service.injectInput.SystemKeys)
	}
	if len(refs) != 2 || tokens != 42 {
		t.Fatalf("refs = %#v tokens = %d", refs, tokens)
	}
	first := refs[0]
	if first.ID != 7001 || first.Title != "支付回调重试约定" || first.Snippet != "回调必须幂等" ||
		first.Scope != "global" || first.Tokens != 12 {
		t.Fatalf("first ref = %#v", first)
	}
	if refs[1].Scope != "system" || refs[1].Tokens != 30 {
		t.Fatalf("second ref = %#v", refs[1])
	}
}

func TestRetrieveForVersionUsesExplicitSystemKeys(t *testing.T) {
	service := &fakeKnowledgeService{}
	systems := &fakeVersionSystemReader{keys: []string{"payment"}}
	adapter := New(service, systems)

	if _, _, err := adapter.RetrieveForVersion(context.Background(), 10, []string{"crm"}); err != nil {
		t.Fatalf("RetrieveForVersion: %v", err)
	}
	if systems.calls != 0 {
		t.Fatalf("explicit system keys must skip the version reader, calls = %d", systems.calls)
	}
	if strings.Join(service.injectInput.SystemKeys, ",") != "crm" {
		t.Fatalf("inject system keys = %v", service.injectInput.SystemKeys)
	}
}

func TestRetrieveForVersionWithoutSystemReaderInjectsGlobalKnowledge(t *testing.T) {
	service := &fakeKnowledgeService{}
	adapter := New(service, nil)

	if _, _, err := adapter.RetrieveForVersion(context.Background(), 10, nil); err != nil {
		t.Fatalf("RetrieveForVersion: %v", err)
	}
	if len(service.injectInput.SystemKeys) != 0 {
		t.Fatalf("system keys = %v, want none", service.injectInput.SystemKeys)
	}
}

func TestRetrieveForVersionPropagatesFailures(t *testing.T) {
	service := &fakeKnowledgeService{injectFn: func(context.Context, knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error) {
		return nil, platform.Unavailable("knowledge repository unavailable")
	}}
	adapter := New(service, nil)
	if _, _, err := adapter.RetrieveForVersion(context.Background(), 10, nil); err == nil {
		t.Fatal("inject failure was swallowed")
	}

	systems := &fakeVersionSystemReader{err: errors.New("version read failed")}
	adapter = New(&fakeKnowledgeService{}, systems)
	if _, _, err := adapter.RetrieveForVersion(context.Background(), 10, nil); err == nil {
		t.Fatal("version system reader failure was swallowed")
	}

	adapter = New(nil, nil)
	if _, _, err := adapter.RetrieveForVersion(context.Background(), 10, nil); err == nil {
		t.Fatal("unwired knowledge service must fail")
	}
}

func TestRecordInjectionMapsRefs(t *testing.T) {
	service := &fakeKnowledgeService{}
	adapter := New(service, nil)

	refs := []agentapp.KnowledgeRef{
		{ID: 7001, Title: "支付回调重试约定", Snippet: "回调必须幂等", Scope: "global", Tokens: 12},
		{ID: 7002, Title: "统一登录", Snippet: "复用 SSO", Scope: "system", Tokens: 30},
	}
	if err := adapter.RecordInjection(context.Background(), 77, 1001, refs); err != nil {
		t.Fatalf("RecordInjection: %v", err)
	}
	if service.usageCalls != 1 || service.usageRunID != 77 || service.usageItemID != 1001 {
		t.Fatalf("usage call = %d run = %d item = %d", service.usageCalls, service.usageRunID, service.usageItemID)
	}
	if len(service.usageItems) != 2 {
		t.Fatalf("usage items = %#v", service.usageItems)
	}
	item := service.usageItems[1]
	if item.ID != 7002 || item.Scope != knowledge.ScopeSystem || item.Tokens != 30 ||
		item.Title != "统一登录" || item.Snippet != "复用 SSO" {
		t.Fatalf("usage item = %#v", item)
	}
}

func TestRecordInjectionEmptyRefsIsNoop(t *testing.T) {
	service := &fakeKnowledgeService{}
	adapter := New(service, nil)
	if err := adapter.RecordInjection(context.Background(), 77, 1001, nil); err != nil {
		t.Fatalf("RecordInjection: %v", err)
	}
	if service.usageCalls != 0 {
		t.Fatalf("usage calls = %d, want 0", service.usageCalls)
	}
}

func TestProposeKnowledgeForcesDraftSourceAndRunActor(t *testing.T) {
	service := &fakeKnowledgeService{createFn: func(_ context.Context, _ platform.Actor, in knowledgeapp.CreateInput) (knowledge.Item, error) {
		return knowledge.Item{ID: 7004, Scope: knowledge.Scope(in.Scope), Status: knowledge.StatusDraft, Revision: 1}, nil
	}}
	adapter := New(service, nil)
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 7, Role: "admin"})

	result, err := adapter.ProposeKnowledge(ctx, 10, 77, tools.KnowledgeProposal{
		Title: "支付回调重试约定", Content: "回调必须幂等", Tags: []string{"支付"}, Scope: "system", SystemKey: stringPtr("payment"),
	})
	if err != nil {
		t.Fatalf("ProposeKnowledge: %v", err)
	}
	if service.createCalls != 1 {
		t.Fatalf("create calls = %d", service.createCalls)
	}
	// The AI proposal never auto-approves, even when the run context carries an
	// admin actor and knowledge.review_required=false.
	if service.createActor.Role != "" {
		t.Fatalf("proposal actor role = %q, want none (forced draft)", service.createActor.Role)
	}
	if service.createActor.ID != 7 {
		t.Fatalf("proposal created_by actor = %d, want 7", service.createActor.ID)
	}
	input := service.createInput
	if input.Title != "支付回调重试约定" || input.Content != "回调必须幂等" || input.Scope != knowledge.ScopeSystem {
		t.Fatalf("create input = %#v", input)
	}
	if input.SystemKey == nil || *input.SystemKey != "payment" {
		t.Fatalf("create system_key = %v", input.SystemKey)
	}
	if input.SourceVersionID == nil || *input.SourceVersionID != 10 ||
		input.PromotedFromVersionID == nil || *input.PromotedFromVersionID != 10 {
		t.Fatalf("create source/promoted = %v/%v", input.SourceVersionID, input.PromotedFromVersionID)
	}
	if input.AgentRunID == nil || *input.AgentRunID != 77 {
		t.Fatalf("create agent_run_id = %v", input.AgentRunID)
	}
	if result.ID != 7004 || result.Status != "draft" || result.Scope != "system" || result.Revision != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestProposeKnowledgeRejectsInvalidVersion(t *testing.T) {
	service := &fakeKnowledgeService{}
	adapter := New(service, nil)
	_, err := adapter.ProposeKnowledge(context.Background(), 0, 77, tools.KnowledgeProposal{Title: "标题", Content: "内容"})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("error = %v, want platform invalid_argument", err)
	}
	if service.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", service.createCalls)
	}
}

func stringPtr(value string) *string { return &value }
