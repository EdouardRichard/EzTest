package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- fakes -----------------------------------------------------------------

type fakeRequirementPort struct {
	list       []RequirementSummary
	listErr    error
	detail     RequirementDetail
	getErr     error
	getCalls   int
	getVersion int64
	getID      int64

	created    RequirementSummary
	createErr  error
	createCall int
	createVer  int64
	createIn   RequirementDraft

	updated    RequirementSummary
	updateErr  error
	updateCall int
	updateVer  int64
	updateID   int64
	updateIn   RequirementDraft

	relationErr    error
	relationCall   int
	relationVer    int64
	relationFrom   int64
	relationTo     int64
	relationKind   string
	relationReason string
}

func (f *fakeRequirementPort) ListRequirements(_ context.Context, versionID int64) ([]RequirementSummary, error) {
	f.getVersion = versionID
	return f.list, f.listErr
}

func (f *fakeRequirementPort) GetRequirement(_ context.Context, versionID, requirementID int64) (RequirementDetail, error) {
	f.getCalls++
	f.getVersion = versionID
	f.getID = requirementID
	return f.detail, f.getErr
}

func (f *fakeRequirementPort) CreateDraft(_ context.Context, versionID int64, in RequirementDraft) (RequirementSummary, error) {
	f.createCall++
	f.createVer = versionID
	f.createIn = in
	return f.created, f.createErr
}

func (f *fakeRequirementPort) UpdateDraft(_ context.Context, versionID, requirementID int64, in RequirementDraft) (RequirementSummary, error) {
	f.updateCall++
	f.updateVer = versionID
	f.updateID = requirementID
	f.updateIn = in
	return f.updated, f.updateErr
}

func (f *fakeRequirementPort) ProposeRelation(_ context.Context, versionID, fromID, toID int64, kind, description string) error {
	f.relationCall++
	f.relationVer = versionID
	f.relationFrom = fromID
	f.relationTo = toID
	f.relationKind = kind
	f.relationReason = description
	return f.relationErr
}

type fakeAttachmentPort struct {
	attachments []AttachmentMeta
	listErr     error
	listVer     int64
	listConv    int64

	text     AttachmentText
	textErr  error
	textVer  int64
	textConv int64
	textID   int64
	offset   int
	maxChars int

	image    AttachmentImage
	imageErr error
	imageID  int64
}

func (f *fakeAttachmentPort) ListAttachments(_ context.Context, versionID, conversationID int64) ([]AttachmentMeta, error) {
	f.listVer = versionID
	f.listConv = conversationID
	return f.attachments, f.listErr
}

func (f *fakeAttachmentPort) ReadAttachmentText(_ context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (AttachmentText, error) {
	f.textVer = versionID
	f.textConv = conversationID
	f.textID = attachmentID
	f.offset = offset
	f.maxChars = maxChars
	return f.text, f.textErr
}

func (f *fakeAttachmentPort) AttachmentImageRef(_ context.Context, versionID, conversationID, attachmentID int64) (AttachmentImage, error) {
	f.imageID = attachmentID
	return f.image, f.imageErr
}

type fakeResourcePort struct {
	suts      []SUTSummary
	sutErr    error
	sutVer    int64
	pages     []ResourcePage
	pageErr   error
	pageVer   int64
	pageSUTID int64
}

func (f *fakeResourcePort) ListSUTs(_ context.Context, versionID int64) ([]SUTSummary, error) {
	f.sutVer = versionID
	return f.suts, f.sutErr
}

func (f *fakeResourcePort) ListPages(_ context.Context, versionID, versionSUTID int64) ([]ResourcePage, error) {
	f.pageVer = versionID
	f.pageSUTID = versionSUTID
	return f.pages, f.pageErr
}

func (f *fakeResourcePort) deps() Deps { return Deps{Resource: f} }

func execute(t *testing.T, registry *Registry, scope Scope, name, params string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), scope, name, json.RawMessage(params))
}

func resultMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result
}

// --- requirement tools -----------------------------------------------------

func TestRequirementReadUsesInjectedScope(t *testing.T) {
	port := &fakeRequirementPort{list: []RequirementSummary{
		{ID: 501, CurrentVersionID: 601, Revision: 2, Title: "登录", Status: "draft", Priority: "P1"},
	}}
	registry := NewP1Registry(Deps{Requirement: port})
	result, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementRead, `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.getVersion != 10 {
		t.Fatalf("version = %d, want injected scope version", port.getVersion)
	}
	body := resultMap(t, result)
	if body["count"] != float64(1) {
		t.Fatalf("result = %#v", body)
	}
}

func TestRequirementReadRejectsCrossVersionRequirement(t *testing.T) {
	port := &fakeRequirementPort{getErr: NotFound("requirement")}
	registry := NewP1Registry(Deps{Requirement: port})
	_, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementRead, `{"requirement_id":"501"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeNotFound {
		t.Fatalf("code = %q", toolErr.Code)
	}
	if port.getVersion != 10 || port.getID != 501 {
		t.Fatalf("get = %d/%d", port.getVersion, port.getID)
	}
}

func TestRequirementDraftCreateValidatesAndInjectsScope(t *testing.T) {
	port := &fakeRequirementPort{created: RequirementSummary{ID: 501, CurrentVersionID: 700, Revision: 1, Status: "draft"}}
	registry := NewP1Registry(Deps{Requirement: port})

	result, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementDraftCreate, `{
		"title":"  登录功能  ",
		"description":"支持手机号登录",
		"acceptance_criteria":"手机号可登录",
		"constraints":{"business_rules":["仅限国内手机号"]},
		"target_version_suts":["5001"],
		"target_pages":["/login"],
		"target_apis":["POST /api/login"]
	}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.createCall != 1 || port.createVer != 10 {
		t.Fatalf("create calls/version = %d/%d", port.createCall, port.createVer)
	}
	if port.createIn.Title != "登录功能" || port.createIn.Priority != "P2" {
		t.Fatalf("draft = %#v", port.createIn)
	}
	if string(port.createIn.Constraints) != `{"business_rules":["仅限国内手机号"]}` {
		t.Fatalf("constraints = %s", port.createIn.Constraints)
	}
	body := resultMap(t, result)
	if body["requirement_id"] != "501" || body["version_id"] != "10" || body["revision"] != float64(1) || body["status"] != "draft" {
		t.Fatalf("result = %#v", body)
	}
}

func TestRequirementDraftCreateRejectsInvalidFields(t *testing.T) {
	port := &fakeRequirementPort{}
	registry := NewP1Registry(Deps{Requirement: port})
	cases := []string{
		`{"title":"","description":"d","acceptance_criteria":"a"}`,
		`{"title":"t","description":"","acceptance_criteria":"a"}`,
		`{"title":"t","description":"d","acceptance_criteria":"a","priority":"P9"}`,
		`{"title":"t","description":"d","acceptance_criteria":"a","constraints":[]}`,
		`{"title":"t","description":"d","acceptance_criteria":"a","target_pages":[""]}`,
		`{"title":"t"}`,
	}
	for _, params := range cases {
		if _, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementDraftCreate, params); err == nil {
			t.Fatalf("expected invalid_argument for %s", params)
		} else if toolErr := asToolError(t, err); toolErr.Code != CodeInvalidArgument {
			t.Fatalf("params %s code = %q, want invalid_argument", params, toolErr.Code)
		}
	}
	if port.createCall != 0 {
		t.Fatalf("port called %d times for invalid input", port.createCall)
	}
}

func TestRequirementDraftUpdateOnlyForDraft(t *testing.T) {
	port := &fakeRequirementPort{detail: RequirementDetail{
		RequirementSummary: RequirementSummary{ID: 501, Status: "clarified"},
	}}
	registry := NewP1Registry(Deps{Requirement: port})
	_, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementDraftUpdate, `{
		"requirement_id":"501","title":"t","description":"d","acceptance_criteria":"a"
	}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeStateConflict {
		t.Fatalf("code = %q", toolErr.Code)
	}
	if !strings.Contains(toolErr.Message, "reopen_propose") {
		t.Fatalf("message = %q", toolErr.Message)
	}
	if port.updateCall != 0 {
		t.Fatalf("update called for a clarified requirement")
	}
}

func TestRequirementDraftUpdateProducesNewRevision(t *testing.T) {
	port := &fakeRequirementPort{
		detail:  RequirementDetail{RequirementSummary: RequirementSummary{ID: 501, Status: "draft", Revision: 2}},
		updated: RequirementSummary{ID: 501, CurrentVersionID: 702, Revision: 3, Status: "draft"},
	}
	registry := NewP1Registry(Deps{Requirement: port})
	result, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementDraftUpdate, `{
		"requirement_id":"501","title":"新标题","description":"d","acceptance_criteria":"a","priority":"P0"
	}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.updateCall != 1 || port.updateID != 501 || port.updateVer != 10 {
		t.Fatalf("update = %d %d/%d", port.updateCall, port.updateID, port.updateVer)
	}
	body := resultMap(t, result)
	if body["revision"] != float64(3) || body["status"] != "draft" {
		t.Fatalf("result = %#v", body)
	}
}

func TestRequirementRelationProposeValidates(t *testing.T) {
	port := &fakeRequirementPort{}
	registry := NewP1Registry(Deps{Requirement: port})

	invalid := []string{
		`{"from_requirement_id":"501","to_requirement_id":"501","kind":"depends_on"}`,
		`{"from_requirement_id":"501","to_requirement_id":"502","kind":"blocks"}`,
		`{"from_requirement_id":"x","to_requirement_id":"502","kind":"depends_on"}`,
	}
	for _, params := range invalid {
		_, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementRelationPropose, params)
		if err == nil {
			t.Fatalf("expected invalid_argument for %s", params)
		}
		if toolErr := asToolError(t, err); toolErr.Code != CodeInvalidArgument {
			t.Fatalf("params %s code = %q", params, toolErr.Code)
		}
	}
	if port.relationCall != 0 {
		t.Fatalf("relation port called for invalid input")
	}

	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementRelationPropose, `{
		"from_requirement_id":"501","to_requirement_id":"502","kind":"depends_on","description":"依赖"
	}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.relationCall != 1 || port.relationVer != 10 || port.relationFrom != 501 || port.relationTo != 502 {
		t.Fatalf("relation = %d %d %d->%d", port.relationCall, port.relationVer, port.relationFrom, port.relationTo)
	}
}

func TestRequirementReopenProposeWritesProposalOnly(t *testing.T) {
	var gotScope Scope
	var gotID int64
	var gotReason string
	port := &fakeRequirementPort{detail: RequirementDetail{
		RequirementSummary: RequirementSummary{ID: 501, Status: "clarified"},
	}}
	deps := Deps{
		Requirement: port,
		RollbackProposal: func(_ context.Context, scope Scope, requirementID int64, reason string) (any, error) {
			gotScope = scope
			gotID = requirementID
			gotReason = reason
			return map[string]any{"message_id": "7001"}, nil
		},
	}
	registry := NewP1Registry(deps)
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20, AgentRunID: 30}, ToolRequirementReopenPropose, `{
		"requirement_id":"501","reason":"需求已变化，需要重新澄清"
	}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.getVersion != 10 || port.getID != 501 || gotID != 501 || gotReason == "" ||
		gotScope.VersionID != 10 || gotScope.ConversationID != 20 {
		t.Fatalf("proposal args = %d %q %#v", gotID, gotReason, gotScope)
	}
	body := resultMap(t, result)
	if body["status"] != "recorded" {
		t.Fatalf("result = %#v", body)
	}
}

func TestRequirementReopenProposeRejectsNonClarifiedAndUnknownScope(t *testing.T) {
	deps := Deps{
		Requirement: &fakeRequirementPort{detail: RequirementDetail{
			RequirementSummary: RequirementSummary{ID: 501, Status: "draft"},
		}},
		RollbackProposal: func(context.Context, Scope, int64, string) (any, error) {
			t.Fatal("proposal must not be written for a draft requirement")
			return nil, nil
		},
	}
	registry := NewP1Registry(deps)
	_, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolRequirementReopenPropose,
		`{"requirement_id":"501","reason":"需要重新澄清"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeStateConflict {
		t.Fatalf("code = %q", toolErr.Code)
	}

	outside := &fakeRequirementPort{getErr: NotFound("requirement")}
	registry = NewP1Registry(Deps{
		Requirement: outside,
		RollbackProposal: func(context.Context, Scope, int64, string) (any, error) {
			t.Fatal("proposal must not be written for an out-of-scope requirement")
			return nil, nil
		},
	})
	_, err = execute(t, registry, Scope{VersionID: 11, ConversationID: 20}, ToolRequirementReopenPropose,
		`{"requirement_id":"501","reason":"需要重新澄清"}`)
	if toolErr := asToolError(t, err); toolErr.Code != CodeNotFound {
		t.Fatalf("code = %q", toolErr.Code)
	}
}

// --- attachment tools ------------------------------------------------------

func TestAttachmentListUsesScope(t *testing.T) {
	port := &fakeAttachmentPort{attachments: []AttachmentMeta{
		{ID: 9001, Name: "需求.md", MIME: "text/markdown", Size: 1200, Status: "ready", ConversationID: 20},
	}}
	registry := NewP1Registry(Deps{Attachment: port})
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentList, `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.listVer != 10 || port.listConv != 20 {
		t.Fatalf("scope = %d/%d", port.listVer, port.listConv)
	}
	body := resultMap(t, result)
	attachments := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("result = %#v", body)
	}
	item := attachments[0].(map[string]any)
	if item["id"] != "9001" || item["name"] != "需求.md" || item["mime"] != "text/markdown" {
		t.Fatalf("attachment = %#v", item)
	}
}

func TestAttachmentReadTextPaging(t *testing.T) {
	port := &fakeAttachmentPort{text: AttachmentText{
		AttachmentID: 9001, Name: "需求.md", MIME: "text/markdown",
		Text: "abcdef", Offset: 2, TotalChars: 10, Truncated: true,
	}}
	registry := NewP1Registry(Deps{Attachment: port})
	if _, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentReadText, `{
		"attachment_id":"9001","offset":2,"max_chars":4
	}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.textID != 9001 || port.offset != 2 || port.maxChars != 4 {
		t.Fatalf("read args = %d %d %d", port.textID, port.offset, port.maxChars)
	}

	invalid := []string{
		`{"attachment_id":""}`,
		`{"attachment_id":"9001","offset":-1}`,
		`{"attachment_id":"9001","max_chars":0}`,
		`{"attachment_id":"9001","max_chars":20001}`,
	}
	for _, params := range invalid {
		if _, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentReadText, params); err == nil {
			t.Fatalf("expected invalid_argument for %s", params)
		}
	}
}

func TestAttachmentDefaultMaxChars(t *testing.T) {
	port := &fakeAttachmentPort{}
	registry := NewP1Registry(Deps{Attachment: port})
	if _, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentReadText, `{"attachment_id":"9001"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.maxChars != 8000 {
		t.Fatalf("max chars = %d, want default", port.maxChars)
	}
}

func TestAttachmentImageRefReturnsMetadata(t *testing.T) {
	port := &fakeAttachmentPort{image: AttachmentImage{AttachmentID: 9002, SHA256: "abc", URL: "/api/content/abc", MIME: "image/png"}}
	registry := NewP1Registry(Deps{Attachment: port})
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef, `{"attachment_id":"9002"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.imageID != 9002 {
		t.Fatalf("image id = %d", port.imageID)
	}
	body := resultMap(t, result)
	if body["sha256"] != "abc" || body["url"] != "/api/content/abc" || body["mime"] != "image/png" {
		t.Fatalf("result = %#v", body)
	}
}

// --- resource tools --------------------------------------------------------

func TestResourceToolsUseVersionScope(t *testing.T) {
	port := &fakeResourcePort{
		suts:  []SUTSummary{{VersionSUTID: 5001, Name: "订单中心", Environment: "prod"}},
		pages: []ResourcePage{{VersionSUTID: 5001, Kind: "page", Name: "登录页", Path: "/login"}},
	}
	registry := NewP1Registry(port.deps())

	result, err := execute(t, registry, Scope{VersionID: 10}, ToolSUTList, `{}`)
	if err != nil {
		t.Fatalf("sut.list: %v", err)
	}
	if port.sutVer != 10 {
		t.Fatalf("sut version = %d", port.sutVer)
	}
	body := resultMap(t, result)
	suts := body["suts"].([]any)
	if len(suts) != 1 || suts[0].(map[string]any)["version_sut_id"] != "5001" {
		t.Fatalf("result = %#v", body)
	}

	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolSUTPageList, `{"version_sut_id":"5001"}`); err != nil {
		t.Fatalf("sut.page_list: %v", err)
	}
	if port.pageVer != 10 || port.pageSUTID != 5001 {
		t.Fatalf("page args = %d/%d", port.pageVer, port.pageSUTID)
	}

	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolSUTPageList, `{"version_sut_id":"bad"}`); err == nil {
		t.Fatal("expected invalid_argument for a bad version_sut_id")
	}
}

// --- context tools ---------------------------------------------------------

func TestContextVersionPackAndTaskPackUseScope(t *testing.T) {
	var gotScope Scope
	deps := Deps{
		VersionPack: func(_ context.Context, scope Scope) (any, error) {
			gotScope = scope
			return map[string]any{"version_id": "10", "requirements": []any{}}, nil
		},
		TaskPack: func(_ context.Context, scope Scope) (any, error) {
			return map[string]any{"goal": "澄清登录需求", "conversation_id": "20"}, nil
		},
	}
	registry := NewP1Registry(deps)
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolContextVersionPack, `{}`)
	if err != nil {
		t.Fatalf("version pack: %v", err)
	}
	if gotScope.VersionID != 10 || gotScope.ConversationID != 20 {
		t.Fatalf("scope = %#v", gotScope)
	}
	if resultMap(t, result)["version_id"] != "10" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolContextTaskPack, `{}`); err != nil {
		t.Fatalf("task pack: %v", err)
	}
}

func TestContextTeamRoomReadValidatesMaxPosts(t *testing.T) {
	var gotMax int
	deps := Deps{TeamRoomRead: func(_ context.Context, _ Scope, maxPosts int) (any, error) {
		gotMax = maxPosts
		return map[string]any{"posts": []any{}}, nil
	}}
	registry := NewP1Registry(deps)

	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolContextTeamRoomRead, `{}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotMax != 10 {
		t.Fatalf("default max posts = %d", gotMax)
	}
	if _, err := execute(t, registry, Scope{VersionID: 10}, ToolContextTeamRoomRead, `{"max_posts":51}`); err == nil {
		t.Fatal("expected invalid_argument for max_posts=51")
	}
}

func TestContextSummaryUpdateValidatesAndWrites(t *testing.T) {
	var gotSummary string
	var gotPinned []string
	deps := Deps{SummaryUpdate: func(_ context.Context, _ Scope, summary string, pinned []string) (any, error) {
		gotSummary = summary
		gotPinned = pinned
		return map[string]any{"version": 2}, nil
	}}
	registry := NewP1Registry(deps)

	if _, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolContextSummaryUpdate, `{
		"summary":"用户要求手机号登录","pinned_facts":["支持手机号"]
	}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotSummary != "用户要求手机号登录" || len(gotPinned) != 1 || gotPinned[0] != "支持手机号" {
		t.Fatalf("summary = %q pinned = %#v", gotSummary, gotPinned)
	}

	longSummary := strings.Repeat("长", 8001)
	tooManyPinned := make([]string, 21)
	for i := range tooManyPinned {
		tooManyPinned[i] = "fact"
	}
	invalid := []map[string]any{
		{"summary": longSummary},
		{"summary": "ok", "pinned_facts": tooManyPinned},
		{"summary": "ok", "pinned_facts": []string{strings.Repeat("x", 501)}},
	}
	for _, params := range invalid {
		raw, _ := json.Marshal(params)
		if _, err := execute(t, registry, Scope{VersionID: 10}, ToolContextSummaryUpdate, string(raw)); err == nil {
			t.Fatalf("expected invalid_argument for %s", raw)
		}
	}
}

func TestPortBusinessErrorsBecomeStructuredToolErrors(t *testing.T) {
	port := &fakeRequirementPort{listErr: errors.New("db unavailable")}
	registry := NewP1Registry(Deps{Requirement: port})
	_, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementRead, `{}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("internal port failures must not masquerade as business tool errors: %v", err)
	}
}
