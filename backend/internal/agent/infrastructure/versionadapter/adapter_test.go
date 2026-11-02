package versionadapter_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeRequirements struct {
	listFn     func(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	getFn      func(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
	createFn   func(ctx context.Context, versionID int64, draft versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error)
	updateFn   func(ctx context.Context, versionID, requirementID int64, draft versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error)
	relationFn func(ctx context.Context, versionID int64, in versionapp.RelationInput, actorID int64) (versionapp.RelationView, error)
}

func (f *fakeRequirements) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error) {
	return f.listFn(ctx, versionID, status, limit, beforeID)
}

func (f *fakeRequirements) Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error) {
	return f.getFn(ctx, versionID, requirementID)
}

func (f *fakeRequirements) Create(ctx context.Context, versionID int64, draft versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error) {
	return f.createFn(ctx, versionID, draft, actorID)
}

func (f *fakeRequirements) Update(ctx context.Context, versionID, requirementID int64, draft versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error) {
	return f.updateFn(ctx, versionID, requirementID, draft, reason, actorID)
}

func (f *fakeRequirements) CreateRelation(ctx context.Context, versionID int64, in versionapp.RelationInput, actorID int64) (versionapp.RelationView, error) {
	return f.relationFn(ctx, versionID, in, actorID)
}

type fakeConversations struct {
	getFn       func(ctx context.Context, versionID, conversationID int64) (versionapp.ConversationView, error)
	messagesFn  func(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error)
	summaryFn   func(ctx context.Context, conversationID int64) (versionapp.SummaryView, error)
	appendFn    func(ctx context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyTo *int64) (versionapp.MessageView, error)
	updateFn    func(ctx context.Context, conversationID int64, summary string, pinned []string) error
	reconcileFn func(ctx context.Context, limit int) ([]versionapp.ReconcilerConversation, error)
}

func (f *fakeConversations) Get(ctx context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
	return f.getFn(ctx, versionID, conversationID)
}

func (f *fakeConversations) Messages(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error) {
	return f.messagesFn(ctx, versionID, conversationID, after, limit)
}

func (f *fakeConversations) Summary(ctx context.Context, conversationID int64) (versionapp.SummaryView, error) {
	return f.summaryFn(ctx, conversationID)
}

func (f *fakeConversations) AppendAssistantMessageWithReply(ctx context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyTo *int64) (versionapp.MessageView, error) {
	return f.appendFn(ctx, conversationID, parts, runID, replyTo)
}

func (f *fakeConversations) UpdateSummary(ctx context.Context, conversationID int64, summary string, pinned []string) error {
	return f.updateFn(ctx, conversationID, summary, pinned)
}

func (f *fakeConversations) ListReconcilable(ctx context.Context, limit int) ([]versionapp.ReconcilerConversation, error) {
	return f.reconcileFn(ctx, limit)
}

type fakeVersions struct {
	getFn func(ctx context.Context, id int64) (versionapp.VersionView, error)
}

func (f *fakeVersions) Get(ctx context.Context, id int64) (versionapp.VersionView, error) {
	return f.getFn(ctx, id)
}

type fakeStorage struct {
	uploadFn func(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error)
}

func (f *fakeStorage) Upload(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error) {
	return f.uploadFn(ctx, filename, mime, r, maxBytes, actorID)
}

type fakeAttachments struct {
	extractFn  func(ctx context.Context, attachmentID int64) (versionapp.ExtractionResult, error)
	getInfoFn  func(ctx context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error)
	listFn     func(ctx context.Context, versionID, conversationID int64) ([]versionapp.AttachmentMeta, error)
	readTextFn func(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (string, int, error)
	imageRefFn func(ctx context.Context, versionID, conversationID, attachmentID int64) (versionapp.AttachmentImageRef, error)
	pendingFn  func(ctx context.Context, limit int) ([]versionapp.PendingAttachment, error)
}

func (f *fakeAttachments) Extract(ctx context.Context, attachmentID int64) (versionapp.ExtractionResult, error) {
	return f.extractFn(ctx, attachmentID)
}

func (f *fakeAttachments) GetAttachmentInfo(ctx context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error) {
	return f.getInfoFn(ctx, versionID, attachmentID)
}

func (f *fakeAttachments) ListAttachments(ctx context.Context, versionID, conversationID int64) ([]versionapp.AttachmentMeta, error) {
	return f.listFn(ctx, versionID, conversationID)
}

func (f *fakeAttachments) ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (string, int, error) {
	return f.readTextFn(ctx, versionID, conversationID, attachmentID, offset, maxChars)
}

func (f *fakeAttachments) AttachmentImageRef(ctx context.Context, versionID, conversationID, attachmentID int64) (versionapp.AttachmentImageRef, error) {
	return f.imageRefFn(ctx, versionID, conversationID, attachmentID)
}

func (f *fakeAttachments) ListPendingForIngest(ctx context.Context, limit int) ([]versionapp.PendingAttachment, error) {
	return f.pendingFn(ctx, limit)
}

func NotFoundError(kind string) error { return platform.NotFound(kind) }

func TestRequirementPortMapping(t *testing.T) {
	ctx := context.Background()
	var createdDraft versionapp.RequirementDraft
	var createActor int64
	requirement := versionapp.RequirementView{
		ID: 501, CurrentVersionID: 601, Revision: 2, Title: "Login",
		Status: "draft", Priority: "P1",
		TargetVersionSUTs: []string{"1001"}, TargetPages: []string{"1301"}, TargetAPIs: []string{"1401"},
		UpdatedAt: testTime,
	}
	detail := versionapp.RequirementDetail{
		RequirementView: requirement,
		CurrentVersion: versionapp.RequirementVersionView{
			ID: 601, RequirementID: 501, Revision: 2, Title: "Login", Description: "desc",
			AcceptanceCriteria: "accept", Priority: "P1",
			Constraints:       []byte(`{"business_rules":["x"]}`),
			TargetVersionSUTs: []string{"1001"}, TargetPages: []string{"1301"}, TargetAPIs: []string{"1401"},
		},
	}
	fake := &fakeRequirements{
		listFn: func(_ context.Context, versionID int64, _ string, _ int, _ *int64) ([]versionapp.RequirementView, bool, error) {
			if versionID != 10 {
				t.Fatalf("list version = %d", versionID)
			}
			return []versionapp.RequirementView{requirement}, false, nil
		},
		getFn: func(_ context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error) {
			if versionID != 10 || requirementID != 501 {
				return versionapp.RequirementDetail{}, NotFoundError("requirement")
			}
			return detail, nil
		},
		createFn: func(_ context.Context, versionID int64, draft versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error) {
			createdDraft = draft
			createActor = actorID
			return detail, nil
		},
		updateFn: func(_ context.Context, versionID, requirementID int64, draft versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error) {
			return detail, nil
		},
		relationFn: func(_ context.Context, versionID int64, in versionapp.RelationInput, actorID int64) (versionapp.RelationView, error) {
			if in.FromRequirementID != 1 || in.ToRequirementID != 2 || in.Kind != "depends_on" || actorID != 0 {
				t.Fatalf("relation input = %#v actor=%d", in, actorID)
			}
			return versionapp.RelationView{ID: 9}, nil
		},
	}
	adapter := versionadapter.New(fake, nil, nil, nil, nil)

	summaries, err := adapter.ListRequirements(ctx, 10)
	if err != nil || len(summaries) != 1 || summaries[0].ID != 501 || summaries[0].Revision != 2 {
		t.Fatalf("ListRequirements = %#v, %v", summaries, err)
	}
	loaded, err := adapter.GetRequirement(ctx, 10, 501)
	if err != nil {
		t.Fatalf("GetRequirement: %v", err)
	}
	if loaded.Description != "desc" || loaded.AcceptanceCriteria != "accept" || string(loaded.Constraints) != `{"business_rules":["x"]}` {
		t.Fatalf("detail = %#v", loaded)
	}

	summary, err := adapter.CreateDraft(ctx, 10, tools.RequirementDraft{
		Title: "T", Description: "D", AcceptanceCriteria: "A", Priority: "P1",
		TargetVersionSUTs: []string{"1001"},
	})
	if err != nil || summary.ID != 501 {
		t.Fatalf("CreateDraft = %#v, %v", summary, err)
	}
	if createActor != 0 || createdDraft.Title != "T" || len(createdDraft.TargetVersionSUTs) != 1 {
		t.Fatalf("created draft = %#v actor=%d", createdDraft, createActor)
	}
	if string(createdDraft.Constraints) != `{}` {
		t.Fatalf("default constraints = %s", createdDraft.Constraints)
	}
	if err := adapter.ProposeRelation(ctx, 10, 1, 2, "depends_on", "why"); err != nil {
		t.Fatalf("ProposeRelation: %v", err)
	}
}

func TestConversationPortSnapshotAndMapping(t *testing.T) {
	ctx := context.Background()
	summaryText := "滚动摘要"
	conversation := versionapp.ConversationView{ID: 20, VersionID: 10, Summary: &summaryText}
	messages := []versionapp.MessageView{
		{
			ID: 1, ConversationID: 20, Role: "user", CreatedAt: testTime,
			Parts: []version.MessagePart{{Type: "text", Text: "hello"}},
		},
		{
			ID: 2, ConversationID: 20, Role: "assistant", CreatedAt: testTime,
			Parts: []version.MessagePart{
				{Type: "text", Text: "draft created"},
				{Type: "requirement_card", RequirementID: 501, Title: "Login", Status: "draft", Revision: 1},
			},
		},
	}
	fake := &fakeConversations{
		getFn: func(_ context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
			if versionID != 10 || conversationID != 20 {
				return versionapp.ConversationView{}, NotFoundError("conversation")
			}
			return conversation, nil
		},
		summaryFn: func(_ context.Context, conversationID int64) (versionapp.SummaryView, error) {
			return versionapp.SummaryView{
				ConversationID: 20, Summary: "rolling", PinnedFacts: []string{"手机号登录"},
				TokenEstimate: 20, Version: 2, UpdatedAt: testTime,
			}, nil
		},
		messagesFn: func(_ context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error) {
			return messages, false, nil
		},
	}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	snapshot, err := adapter.Snapshot(ctx, 10, 20, 1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Summary != "rolling" || len(snapshot.PinnedFacts) != 1 || len(snapshot.Messages) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Messages[0].ID != 1 || snapshot.Messages[0].Text != "hello" ||
		snapshot.Messages[1].ID != 2 || snapshot.Messages[1].Text != "draft created" {
		t.Fatalf("messages = %#v", snapshot.Messages)
	}
	if len(snapshot.Messages[1].Parts) != 2 {
		t.Fatalf("parts = %#v", snapshot.Messages[1].Parts)
	}
	card := snapshot.Messages[1].Parts[1]
	if card.Type != "requirement_card" || card.Data["requirement_id"] != "501" || card.Data["revision"] != 1 {
		t.Fatalf("card part = %#v", card)
	}

	full, err := adapter.Snapshot(ctx, 10, 20, 0)
	if err != nil || len(full.Messages) != 2 {
		t.Fatalf("full snapshot = %#v, %v", full, err)
	}
}

func TestConversationPortAppendSummaryAndGetMessage(t *testing.T) {
	ctx := context.Background()
	var appended []version.MessagePart
	var appendedRun int64
	var appendedReply *int64
	var updatedSummary string
	var updatedPinned []string
	fake := &fakeConversations{
		getFn: func(_ context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
			return versionapp.ConversationView{ID: 20, VersionID: 10}, nil
		},
		summaryFn: func(_ context.Context, conversationID int64) (versionapp.SummaryView, error) {
			return versionapp.SummaryView{ConversationID: 20, Summary: updatedSummary, PinnedFacts: updatedPinned, Version: 3, UpdatedAt: testTime}, nil
		},
		messagesFn: func(_ context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error) {
			if after == 6 {
				return []versionapp.MessageView{{ID: 7, ConversationID: 20, Role: "user"}}, false, nil
			}
			return []versionapp.MessageView{{ID: 8, ConversationID: 20, Role: "user"}}, false, nil
		},
		appendFn: func(_ context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyTo *int64) (versionapp.MessageView, error) {
			appended = parts
			appendedRun = runID
			appendedReply = replyTo
			return versionapp.MessageView{ID: 30, ConversationID: conversationID, Role: "assistant", Parts: parts, CreatedAt: testTime}, nil
		},
		updateFn: func(_ context.Context, conversationID int64, summary string, pinned []string) error {
			updatedSummary = summary
			updatedPinned = pinned
			return nil
		},
	}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	message, err := adapter.GetMessage(ctx, 10, 20, 7)
	if err != nil || message.ID != 7 {
		t.Fatalf("GetMessage = %#v, %v", message, err)
	}
	if _, err := adapter.GetMessage(ctx, 10, 20, 99); err == nil {
		t.Fatal("GetMessage accepted a missing message")
	} else if platform.AsError(err).Status != 404 {
		t.Fatalf("GetMessage error = %v", err)
	}

	replyTo := int64(7)
	appendedMessage, err := adapter.AppendAssistantMessage(ctx, agentapp.AssistantMessageInput{
		VersionID: 10, ConversationID: 20, AgentRunID: 40, ReplyToMessageID: &replyTo,
		Parts: []agentapp.ConversationPart{
			{Type: "text", Text: "reply"},
			{Type: "requirement_card", Data: map[string]any{
				"requirement_id": "501", "title": "Login", "status": "draft", "revision": float64(1),
			}},
			{Type: "rollback_proposal", Data: map[string]any{"requirement_id": "501", "reason": "why"}},
		},
	})
	if err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	if appendedRun != 40 || len(appended) != 3 {
		t.Fatalf("appended = %#v run=%d", appended, appendedRun)
	}
	if appendedReply == nil || *appendedReply != 7 {
		t.Fatalf("persisted reply = %#v", appendedReply)
	}
	if appended[1].RequirementID != 501 || appended[1].Title != "Login" || appended[1].Revision != 1 {
		t.Fatalf("card = %#v", appended[1])
	}
	if appended[2].Reason != "why" || appended[2].RequirementID != 501 {
		t.Fatalf("rollback = %#v", appended[2])
	}
	if appendedMessage.ReplyToMessageID == nil || *appendedMessage.ReplyToMessageID != 7 {
		t.Fatalf("reply = %#v", appendedMessage.ReplyToMessageID)
	}

	stored, err := adapter.UpdateSummary(ctx, 10, 20, "new summary", []string{"fact"})
	if err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}
	if updatedSummary != "new summary" || len(updatedPinned) != 1 || stored.Version != 3 || stored.Summary != "new summary" {
		t.Fatalf("summary = %#v updated=%q/%#v", stored, updatedSummary, updatedPinned)
	}

	bad := agentapp.AssistantMessageInput{
		VersionID: 10, ConversationID: 20,
		Parts: []agentapp.ConversationPart{{Type: "requirement_card", Data: map[string]any{}}},
	}
	if _, err := adapter.AppendAssistantMessage(ctx, bad); err == nil {
		t.Fatal("AppendAssistantMessage accepted a card without requirement_id")
	}
}

func TestVersionReaderBuildsPack(t *testing.T) {
	ctx := context.Background()
	versions := &fakeVersions{getFn: func(_ context.Context, id int64) (versionapp.VersionView, error) {
		return versionapp.VersionView{
			ID: 10, Title: "Orders", Status: "running",
			Bindings: []versionapp.BindingView{{
				ID:          1001,
				SUT:         resourceapp.SUTView{ID: "1001", Name: "Orders"},
				Environment: resourceapp.EnvironmentView{ID: "1101", SUTID: "1001", Name: "prod", BaseURL: "https://example.test"},
				Pages: []resourceapp.PageView{
					{ID: "1301", SUTID: "1001", EnvironmentID: "1101", Name: "Login", RouteTemplate: "/login"},
				},
				APIs: []resourceapp.APIView{
					{ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET", PathTemplate: "/api/login", Name: "Login API"},
				},
			}},
		}, nil
	}}
	requirements := &fakeRequirements{
		listFn: func(context.Context, int64, string, int, *int64) ([]versionapp.RequirementView, bool, error) {
			return []versionapp.RequirementView{{ID: 501, Title: "Login", Status: "draft", Priority: "P1"}}, false, nil
		},
		getFn: func(context.Context, int64, int64) (versionapp.RequirementDetail, error) {
			return versionapp.RequirementDetail{Attachments: []versionapp.AttachmentView{
				{ID: 1, Name: "spec.md", MIME: "text/markdown", ExtractionStatus: "done"},
			}}, nil
		},
	}
	adapter := versionadapter.New(requirements, nil, versions, nil, nil)
	pack, err := adapter.VersionPack(ctx, 10)
	if err != nil {
		t.Fatalf("VersionPack: %v", err)
	}
	if pack.VersionID != 10 || pack.Title != "Orders" || pack.Status != "running" {
		t.Fatalf("pack = %#v", pack)
	}
	if len(pack.SUTs) != 1 || !strings.Contains(pack.SUTs[0], "Orders") {
		t.Fatalf("suts = %#v", pack.SUTs)
	}
	if len(pack.Pages) != 1 || !strings.Contains(pack.Pages[0], "/login") {
		t.Fatalf("pages = %#v", pack.Pages)
	}
	if len(pack.APIs) != 1 || !strings.Contains(pack.APIs[0], "/api/login") {
		t.Fatalf("apis = %#v", pack.APIs)
	}
	if len(pack.Requirements) != 1 || !strings.Contains(pack.Requirements[0], "#501") {
		t.Fatalf("requirements = %#v", pack.Requirements)
	}
	if len(pack.Attachments) != 1 || !strings.Contains(pack.Attachments[0], "spec.md") {
		t.Fatalf("attachments = %#v", pack.Attachments)
	}
}

func TestResourcePortMapping(t *testing.T) {
	ctx := context.Background()
	versions := &fakeVersions{getFn: func(_ context.Context, id int64) (versionapp.VersionView, error) {
		return versionapp.VersionView{
			ID: 10,
			Bindings: []versionapp.BindingView{
				{ID: 1001, SUT: resourceapp.SUTView{ID: "1001", Name: "Orders"}, Environment: resourceapp.EnvironmentView{ID: "1101", Name: "prod"},
					Pages: []resourceapp.PageView{{ID: "1301", Name: "Login", RouteTemplate: "/login"}}},
				{ID: 1002, SUT: resourceapp.SUTView{ID: "1002", Name: "Admin"}, Environment: resourceapp.EnvironmentView{ID: "1102", Name: "staging"},
					APIs: []resourceapp.APIView{{ID: "1401", Method: "GET", PathTemplate: "/api/admin"}}},
			},
		}, nil
	}}
	adapter := versionadapter.New(nil, nil, versions, nil, nil)
	suts, err := adapter.ListSUTs(ctx, 10)
	if err != nil || len(suts) != 2 || suts[0].VersionSUTID != 1001 || suts[0].Environment != "prod" {
		t.Fatalf("ListSUTs = %#v, %v", suts, err)
	}
	pages, err := adapter.ListPages(ctx, 10, 1001)
	if err != nil || len(pages) != 1 || pages[0].Kind != "page" || pages[0].Path != "/login" {
		t.Fatalf("ListPages = %#v, %v", pages, err)
	}
	all, err := adapter.ListPages(ctx, 10, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListPages all = %#v, %v", all, err)
	}
	if _, err := adapter.ListPages(ctx, 10, 9999); err == nil {
		t.Fatal("ListPages accepted an unknown version SUT")
	} else if platform.AsError(err).Status != 404 {
		t.Fatalf("error = %v", err)
	}
}

func TestContentWriterSpillsText(t *testing.T) {
	ctx := context.Background()
	var received []byte
	storageFake := &fakeStorage{uploadFn: func(_ context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error) {
		received, _ = io.ReadAll(r)
		if !strings.HasSuffix(filename, ".txt") || mime != "text/plain" || actorID != 0 {
			t.Fatalf("upload = %q %q actor=%d", filename, mime, actorID)
		}
		return storage.Object{SHA256: strings.Repeat("a", 64), Size: maxBytes, MIME: mime}, nil
	}}
	adapter := versionadapter.New(nil, nil, nil, nil, storageFake)
	ref, err := adapter.PutText(ctx, "tool result/übersicht", []byte("hello"))
	if err != nil {
		t.Fatalf("PutText: %v", err)
	}
	if string(received) != "hello" || ref.SHA256 != strings.Repeat("a", 64) || ref.Ref != ref.SHA256 {
		t.Fatalf("ref = %#v received=%q", ref, received)
	}

	empty := versionadapter.New(nil, nil, nil, nil, nil)
	if _, err := empty.PutText(ctx, "x", []byte("y")); err == nil {
		t.Fatal("PutText succeeded without storage")
	}
}

func TestAttachmentReaderMapsInfo(t *testing.T) {
	ctx := context.Background()
	messageID := int64(77)
	requirementID := int64(501)
	fake := &fakeAttachments{
		extractFn: func(_ context.Context, attachmentID int64) (versionapp.ExtractionResult, error) {
			return versionapp.ExtractionResult{}, nil
		},
		getInfoFn: func(_ context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error) {
			if versionID != 10 || attachmentID != 9 {
				return versionapp.AttachmentInfo{}, platform.NotFound("attachment")
			}
			return versionapp.AttachmentInfo{
				ID: 9, VersionID: 10, ConversationMessageID: &messageID, RequirementID: &requirementID,
				Name: "spec.md", MIME: "text/markdown", Size: 12,
				Status: "failed", ErrorCode: "unsupported_attachment_format", ErrorText: "legacy doc",
			}, nil
		},
	}
	adapter := versionadapter.New(nil, nil, nil, fake, nil)

	info, err := adapter.GetAttachment(ctx, 10, 9)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if info.ID != 9 || info.RequirementID != 501 || info.Status != "failed" {
		t.Fatalf("info = %#v", info)
	}
	if !strings.Contains(info.Error, "unsupported_attachment_format") || !strings.Contains(info.Error, "legacy doc") {
		t.Fatalf("error = %q", info.Error)
	}
	if info.Name != "spec.md" || info.MIME != "text/markdown" {
		t.Fatalf("info = %#v", info)
	}
	if _, err := adapter.GetAttachment(ctx, 10, 999); err == nil {
		t.Fatal("GetAttachment accepted a missing attachment")
	}
}

func TestAttachmentPortMapsListReadImage(t *testing.T) {
	ctx := context.Background()
	requirementID := int64(501)
	messageID := int64(77)
	sha := strings.Repeat("b", 64)
	var readOffset, readMax int
	fake := &fakeAttachments{
		listFn: func(_ context.Context, versionID, conversationID int64) ([]versionapp.AttachmentMeta, error) {
			if versionID != 10 || conversationID != 20 {
				return nil, platform.NotFound("conversation")
			}
			return []versionapp.AttachmentMeta{{
				ID: 9, ConversationMessageID: messageID, RequirementID: &requirementID,
				Name: "spec.md", MIME: "text/markdown", Size: 12, ExtractionStatus: "done",
			}}, nil
		},
		getInfoFn: func(_ context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error) {
			return versionapp.AttachmentInfo{ID: 9, Name: "spec.md", MIME: "text/markdown", Status: "done"}, nil
		},
		readTextFn: func(_ context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (string, int, error) {
			readOffset, readMax = offset, maxChars
			return "page", 10, nil
		},
		imageRefFn: func(_ context.Context, versionID, conversationID, attachmentID int64) (versionapp.AttachmentImageRef, error) {
			return versionapp.AttachmentImageRef{AttachmentID: 9, SHA256: sha, Name: "login.png", MIME: "image/png", Size: 5}, nil
		},
	}
	adapter := versionadapter.New(nil, nil, nil, fake, nil)

	metas, err := adapter.ListAttachments(ctx, 10, 20)
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListAttachments = %#v, %v", metas, err)
	}
	if metas[0].ID != 9 || metas[0].ConversationID != 20 || metas[0].RequirementID != 501 || metas[0].Status != "done" {
		t.Fatalf("meta = %#v", metas[0])
	}

	text, err := adapter.ReadAttachmentText(ctx, 10, 20, 9, 2, 4)
	if err != nil {
		t.Fatalf("ReadAttachmentText: %v", err)
	}
	if readOffset != 2 || readMax != 4 {
		t.Fatalf("read args = %d/%d", readOffset, readMax)
	}
	if text.Text != "page" || text.TotalChars != 10 || !text.Truncated || text.Name != "spec.md" {
		t.Fatalf("text = %#v", text)
	}

	image, err := adapter.AttachmentImageRef(ctx, 10, 20, 9)
	if err != nil {
		t.Fatalf("AttachmentImageRef: %v", err)
	}
	if image.AttachmentID != 9 || image.SHA256 != sha || image.URL != "/api/content/"+sha || image.MIME != "image/png" {
		t.Fatalf("image = %#v", image)
	}

	empty := versionadapter.New(nil, nil, nil, nil, nil)
	if _, err := empty.GetAttachment(ctx, 10, 9); err == nil {
		t.Fatal("GetAttachment succeeded without attachment service")
	}
	if _, err := empty.ListAttachments(ctx, 10, 20); err == nil {
		t.Fatal("ListAttachments succeeded without attachment service")
	}
}

func TestGetAttachmentRunsIdempotentExtraction(t *testing.T) {
	ctx := context.Background()
	var extracted int64
	var extractedCalls int
	fake := &fakeAttachments{
		extractFn: func(_ context.Context, attachmentID int64) (versionapp.ExtractionResult, error) {
			extracted = attachmentID
			extractedCalls++
			return versionapp.ExtractionResult{Chars: 5}, nil
		},
		getInfoFn: func(_ context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error) {
			return versionapp.AttachmentInfo{
				ID: 9, VersionID: 10, Name: "login-spec.md", MIME: "text/markdown",
				Status: "done", Text: "hello", TotalChars: 5,
			}, nil
		},
	}
	adapter := versionadapter.New(nil, nil, nil, fake, nil)

	info, err := adapter.GetAttachment(ctx, 10, 9)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if extracted != 9 || extractedCalls != 1 {
		t.Fatalf("extract calls = %d id=%d", extractedCalls, extracted)
	}
	if info.Status != "done" || info.Text != "hello" || info.TotalChars != 5 {
		t.Fatalf("info = %#v", info)
	}

	// A failed parse is a domain outcome: no Go error, failed status/error is
	// returned to the Scout instead of retrying forever.
	failed := &fakeAttachments{
		extractFn: func(_ context.Context, attachmentID int64) (versionapp.ExtractionResult, error) {
			return versionapp.ExtractionResult{ErrorCode: "unsupported_attachment_format"}, nil
		},
		getInfoFn: func(_ context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error) {
			return versionapp.AttachmentInfo{
				ID: 9, VersionID: 10, Name: "legacy.doc", MIME: "application/msword",
				Status: "failed", ErrorCode: "unsupported_attachment_format", ErrorText: "legacy doc",
			}, nil
		},
	}
	failedAdapter := versionadapter.New(nil, nil, nil, failed, nil)
	info, err = failedAdapter.GetAttachment(ctx, 10, 9)
	if err != nil {
		t.Fatalf("failed extraction returned an error: %v", err)
	}
	if info.Status != "failed" || !strings.Contains(info.Error, "unsupported_attachment_format") {
		t.Fatalf("info = %#v", info)
	}

	// Infrastructure failures stay retryable Go errors.
	broken := &fakeAttachments{
		extractFn: func(_ context.Context, attachmentID int64) (versionapp.ExtractionResult, error) {
			return versionapp.ExtractionResult{}, &platform.Error{Status: 500, Code: "internal"}
		},
	}
	if _, err := versionadapter.New(nil, nil, nil, broken, nil).GetAttachment(ctx, 10, 9); err == nil {
		t.Fatal("extraction infrastructure error was swallowed")
	}
}

func TestReconcilerPortsMapping(t *testing.T) {
	ctx := context.Background()
	conversations := &fakeConversations{
		reconcileFn: func(_ context.Context, limit int) ([]versionapp.ReconcilerConversation, error) {
			if limit != 100 {
				t.Fatalf("limit = %d", limit)
			}
			return []versionapp.ReconcilerConversation{{
				ID: 20, VersionID: 10,
				Messages: []versionapp.ReconcilerMessage{
					{ID: 1, Role: "user"},
					{ID: 2, Role: "assistant", AgentProfileKey: "clarification_host"},
				},
			}}, nil
		},
	}
	attachments := &fakeAttachments{
		pendingFn: func(_ context.Context, limit int) ([]versionapp.PendingAttachment, error) {
			return []versionapp.PendingAttachment{
				{ID: 5, VersionID: 10, ConversationID: 20, MessageID: 7},
				{ID: 6, VersionID: 10},
			}, nil
		},
	}
	adapter := versionadapter.New(nil, conversations, nil, attachments, nil)

	reader, ok := interface{}(adapter).(agentapp.ConversationReader)
	if !ok {
		t.Fatal("adapter does not implement agentapp.ConversationReader")
	}
	items, err := reader.ListConversations(ctx, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListConversations = %#v, %v", items, err)
	}
	if items[0].ID != 20 || items[0].VersionID != 10 || len(items[0].Messages) != 2 {
		t.Fatalf("conversation = %#v", items[0])
	}
	if items[0].Messages[1].AgentProfileKey != "clarification_host" || items[0].Messages[1].Role != "assistant" {
		t.Fatalf("message = %#v", items[0].Messages[1])
	}

	pendingReader, ok := interface{}(adapter).(agentapp.PendingAttachmentReader)
	if !ok {
		t.Fatal("adapter does not implement agentapp.PendingAttachmentReader")
	}
	pending, err := pendingReader.ListPendingAttachments(ctx, 100)
	if err != nil || len(pending) != 2 {
		t.Fatalf("ListPendingAttachments = %#v, %v", pending, err)
	}
	if pending[0].ID != 5 || pending[0].ConversationID != 20 || pending[0].MessageID != 7 {
		t.Fatalf("pending = %#v", pending[0])
	}
	if pending[1].ConversationID != 0 || pending[1].MessageID != 0 {
		t.Fatalf("unlinked pending = %#v", pending[1])
	}

	empty := versionadapter.New(nil, nil, nil, nil, nil)
	if _, err := empty.ListConversations(ctx, 10); err == nil {
		t.Fatal("ListConversations succeeded without conversation service")
	}
	if _, err := empty.ListPendingAttachments(ctx, 10); err == nil {
		t.Fatal("ListPendingAttachments succeeded without attachment service")
	}
}

func TestMissingDependenciesFailLoudly(t *testing.T) {
	adapter := versionadapter.New(nil, nil, nil, nil, nil)
	if _, err := adapter.VersionPack(context.Background(), 10); err == nil {
		t.Fatal("VersionPack without versions service did not fail")
	}
	if _, err := adapter.ListRequirements(context.Background(), 10); err == nil {
		t.Fatal("ListRequirements without requirement service did not fail")
	}
	if _, err := adapter.Snapshot(context.Background(), 10, 20, 5); err == nil {
		t.Fatal("Snapshot without conversation service did not fail")
	}
	if _, err := adapter.ListSUTs(context.Background(), 10); err == nil {
		t.Fatal("ListSUTs without version service did not fail")
	}
}
