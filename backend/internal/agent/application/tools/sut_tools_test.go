package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeSUTReaderPort is a ResourcePort that also implements the additive SUTReader
// surface consumed by sut.read (the DatabaseLister precedent: a separate
// interface so existing resource ports and fakes keep compiling).
type fakeSUTReaderPort struct {
	fakeResourcePort
	detail  SUTDetail
	readErr error
	readVer int64
	readSUT int64
}

func (f *fakeSUTReaderPort) ReadSUT(_ context.Context, versionID, sutID int64) (SUTDetail, error) {
	f.readVer = versionID
	f.readSUT = sutID
	return f.detail, f.readErr
}

// TestSUTReadDetail pins the sut.read detail tool (SC-F2): it returns the SUT's
// base_url/source/status plus its effective asset summary, so the execution team
// can read a catalog SUT in full before matching it against the case's
// sut_description. The additive tool is registered by the P4 group mechanism the
// runtime registry composes, not the frozen P1 order.
func TestSUTReadDetail(t *testing.T) {
	port := &fakeSUTReaderPort{detail: SUTDetail{
		SUTID: 9001, Name: "订单系统", Intro: "订单管理后台",
		BaseURL: "https://orders.example.com", Source: "admin", Status: "approved",
		Environment: "测试环境", EnvironmentID: 7001,
		Pages:     []ResourcePage{{SUTID: 9001, ID: "101", Kind: "page", Name: "下单页", Path: "/checkout"}},
		APIs:      []ResourcePage{{SUTID: 9001, ID: "201", Kind: "api", Name: "创建订单", Path: "/api/orders", Method: "POST"}},
		Databases: []DatabaseSummary{{ID: "1501", SUTID: 9001, Name: "order_db", Kind: "postgres", ReadOnly: false}},
	}}
	registry := NewKnowledgeRegistry(Deps{Resource: port})
	tool, ok := registry.Get(ToolSUTRead)
	if !ok {
		t.Fatalf("NewKnowledgeRegistry is missing %q when the resource port implements SUTReader", ToolSUTRead)
	}
	if !strings.Contains(tool.Description(), "base_url") {
		t.Fatalf("sut.read description must name base_url: %s", tool.Description())
	}
	// Without the SUTReader surface the tool stays unregistered (never half-wired).
	if _, ok := NewKnowledgeRegistry(Deps{Resource: &fakeResourcePort{}}).Get(ToolSUTRead); ok {
		t.Fatal("sut.read must stay unregistered when the resource port lacks SUTReader")
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolSUTRead); ok {
		t.Fatal("sut.read must stay unregistered without a resource port")
	}

	result, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTRead,
		json.RawMessage(`{"sut_id":"9001"}`))
	if err != nil {
		t.Fatalf("sut.read: %v", err)
	}
	if port.readVer != 2001 || port.readSUT != 9001 {
		t.Fatalf("scope = version %d sut %d, want 2001/9001", port.readVer, port.readSUT)
	}
	body := resultMap(t, result)
	if body["sut_id"] != "9001" {
		t.Fatalf("sut_id = %#v (%T), want the STRING \"9001\"", body["sut_id"], body["sut_id"])
	}
	if body["base_url"] != "https://orders.example.com" {
		t.Fatalf("base_url = %#v", body["base_url"])
	}
	if body["source"] != "admin" || body["status"] != "approved" {
		t.Fatalf("source/status = %#v / %#v", body["source"], body["status"])
	}
	if body["name"] != "订单系统" || body["intro"] != "订单管理后台" {
		t.Fatalf("name/intro = %#v / %#v", body["name"], body["intro"])
	}
	pages, _ := body["pages"].([]any)
	apis, _ := body["apis"].([]any)
	if len(pages) != 1 || len(apis) != 1 {
		t.Fatalf("asset summary pages=%d apis=%d, want 1/1", len(pages), len(apis))
	}
	firstPage, _ := pages[0].(map[string]any)
	if firstPage["id"] != "101" || firstPage["name"] != "下单页" {
		t.Fatalf("page entry = %#v", firstPage)
	}
	firstAPI, _ := apis[0].(map[string]any)
	if firstAPI["id"] != "201" || firstAPI["method"] != "POST" {
		t.Fatalf("api entry = %#v", firstAPI)
	}
}

// TestSUTReadRejectsMalformedSUTID: a malformed required sut_id is a model-fixable
// retryable argument error carrying the top-level field name, and never reaches
// the resource port.
func TestSUTReadRejectsMalformedSUTID(t *testing.T) {
	for _, payload := range []string{`{"sut_id":"abc"}`, `{"sut_id":""}`, `{}`} {
		t.Run(payload, func(t *testing.T) {
			port := &fakeSUTReaderPort{detail: SUTDetail{SUTID: 9001}}
			registry := NewKnowledgeRegistry(Deps{Resource: port})
			_, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTRead, json.RawMessage(payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("malformed sut_id error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != "sut_id" {
				t.Fatalf("field = %q, want top-level sut_id", toolErr.Field)
			}
			if port.readVer != 0 {
				t.Fatalf("malformed argument reached the resource port: versionID=%d", port.readVer)
			}
		})
	}
}

// TestSUTReadNotFound: an unknown / out-of-effective-scope SUT id is a typed
// not_found (the adapter gates on EffectiveSUTs, so a proposed/rejected SUT is
// invisible exactly like an id of another version).
func TestSUTReadNotFound(t *testing.T) {
	port := &fakeSUTReaderPort{readErr: NotFound("sut")}
	registry := NewKnowledgeRegistry(Deps{Resource: port})
	_, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTRead, json.RawMessage(`{"sut_id":"9999"}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeNotFound {
		t.Fatalf("error = %+v, want not_found", toolErr)
	}
}

// TestSUTListEnriched pins SC-F2: sut.list output carries base_url/source/status
// (fixing the Description that claimed base_url while the output omitted it). The
// output now matches what the Description promises.
func TestSUTListEnriched(t *testing.T) {
	port := &fakeResourcePort{suts: []SUTSummary{
		{SUTID: 9001, Name: "订单系统", Environment: "测试环境", Scope: "1 pages, 1 apis",
			BaseURL: "https://orders.example.com", Source: "admin", Status: "approved"},
	}}
	registry := NewKnowledgeRegistry(Deps{Resource: port})
	tool, ok := registry.Get(ToolSUTList)
	if !ok {
		t.Fatal("sut.list must be registered")
	}
	if !strings.Contains(tool.Description(), "base_url") {
		t.Fatalf("sut.list description must name base_url: %s", tool.Description())
	}
	result, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTList, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("sut.list: %v", err)
	}
	body := resultMap(t, result)
	suts, _ := body["suts"].([]any)
	if len(suts) != 1 {
		t.Fatalf("suts = %#v", body["suts"])
	}
	entry, _ := suts[0].(map[string]any)
	if entry["sut_id"] != "9001" {
		t.Fatalf("sut_id = %#v, want string 9001", entry["sut_id"])
	}
	if entry["base_url"] != "https://orders.example.com" {
		t.Fatalf("base_url = %#v, want the enriched value", entry["base_url"])
	}
	if entry["source"] != "admin" || entry["status"] != "approved" {
		t.Fatalf("source/status = %#v / %#v, want enriched values", entry["source"], entry["status"])
	}
}

// fakeAssetRegistrar is the additive P17 registration port stub. It records the
// parsed candidate + injected scope so a test can pin the tool's six surfaces.
type fakeAssetRegistrar struct {
	in     AssetRegisterInput
	scope  Scope
	called int
	result any
	err    error
}

func (f *fakeAssetRegistrar) RegisterAsset(_ context.Context, scope Scope, in AssetRegisterInput) (any, error) {
	f.called++
	f.scope = scope
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return map[string]any{"asset_id": "7001", "status": "approved", "source": "ai"}, nil
}

// TestSUTAssetRegisterTool pins the sut.asset_register tool (SC-F3): it is
// registered only when the registration port is wired (never half-wired), decodes
// the candidate, parses sut_id, and forwards the parsed input + injected scope to
// the server-side gate. The Description names the sha256 evidence gate.
func TestSUTAssetRegisterTool(t *testing.T) {
	reg := &fakeAssetRegistrar{}
	registry := NewKnowledgeRegistry(Deps{AssetRegistration: AssetRegistrationDeps{Registrar: reg}})
	tool, ok := registry.Get(ToolSUTAssetRegister)
	if !ok {
		t.Fatalf("NewKnowledgeRegistry is missing %q when the registration port is wired", ToolSUTAssetRegister)
	}
	if !strings.Contains(tool.Description(), "sha256") {
		t.Fatalf("sut.asset_register description must name the sha256 evidence gate: %s", tool.Description())
	}
	// Runtime cleanliness (AGENTS §2.2): the prompt-rendering Description must
	// not leak development-stage codenames or governance decision ids.
	for _, codename := range []string{"P17", "GD-01", "R-F1"} {
		if strings.Contains(tool.Description(), codename) {
			t.Fatalf("sut.asset_register description leaks the codename %q: %s", codename, tool.Description())
		}
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolSUTAssetRegister); ok {
		t.Fatal("sut.asset_register must stay unregistered without a registration port")
	}

	result, err := registry.Execute(context.Background(),
		Scope{VersionID: 2001, CaseRunID: 5001, AgentRunID: 3001}, ToolSUTAssetRegister,
		json.RawMessage(`{"sut_id":"9001","asset_kind":"page","name":"订单页","url":"https://orders.example.com/orders","evidence_refs":["sha-proof"]}`))
	if err != nil {
		t.Fatalf("sut.asset_register: %v", err)
	}
	if reg.called != 1 {
		t.Fatalf("registrar called %d times, want 1", reg.called)
	}
	if reg.in.SUTID != 9001 || reg.in.AssetKind != "page" || reg.in.Name != "订单页" || reg.in.URL != "https://orders.example.com/orders" {
		t.Fatalf("parsed candidate = %+v", reg.in)
	}
	if len(reg.in.EvidenceRefs) != 1 || reg.in.EvidenceRefs[0] != "sha-proof" {
		t.Fatalf("evidence_refs = %v, want [sha-proof]", reg.in.EvidenceRefs)
	}
	if reg.scope.VersionID != 2001 || reg.scope.CaseRunID != 5001 || reg.scope.AgentRunID != 3001 {
		t.Fatalf("injected scope = %+v, want version 2001 case_run 5001 run 3001", reg.scope)
	}
	body := resultMap(t, result)
	if body["status"] != "approved" || body["asset_id"] != "7001" {
		t.Fatalf("result = %#v, want approved asset 7001", body)
	}
}

// TestSUTAssetRegisterRejectsBadArgs: malformed sut_id / unknown asset_kind / empty
// evidence_refs are model-fixable retryable argument errors carrying the top-level
// field name, and never reach the registration gate.
func TestSUTAssetRegisterRejectsBadArgs(t *testing.T) {
	cases := []struct{ payload, field string }{
		{`{"sut_id":"abc","asset_kind":"page","name":"x","evidence_refs":["s"]}`, "sut_id"},
		{`{"sut_id":"9001","asset_kind":"widget","name":"x","evidence_refs":["s"]}`, "asset_kind"},
		{`{"sut_id":"9001","asset_kind":"page","name":"x","evidence_refs":[]}`, "evidence_refs"},
		{`{"sut_id":"9001","asset_kind":"page","name":"x"}`, "evidence_refs"},
	}
	for _, tc := range cases {
		t.Run(tc.field+"/"+tc.payload, func(t *testing.T) {
			reg := &fakeAssetRegistrar{}
			registry := NewKnowledgeRegistry(Deps{AssetRegistration: AssetRegistrationDeps{Registrar: reg}})
			_, err := registry.Execute(context.Background(), Scope{VersionID: 2001, CaseRunID: 5001},
				ToolSUTAssetRegister, json.RawMessage(tc.payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != tc.field {
				t.Fatalf("field = %q, want %q", toolErr.Field, tc.field)
			}
			if reg.called != 0 {
				t.Fatal("a malformed argument reached the registration gate")
			}
		})
	}
}

// TestSUTAssetRegisterGateFailureMaps: the gate's fail-closed 409 (unproven
// discovery) surfaces to the model as a typed state_conflict, not an internal
// failure or a silent success.
func TestSUTAssetRegisterGateFailureMaps(t *testing.T) {
	reg := &fakeAssetRegistrar{err: platform.NewError(409, "evidence_gate_failed", "discovery is not proven")}
	registry := NewKnowledgeRegistry(Deps{AssetRegistration: AssetRegistrationDeps{Registrar: reg}})
	_, err := registry.Execute(context.Background(), Scope{VersionID: 2001, CaseRunID: 5001},
		ToolSUTAssetRegister, json.RawMessage(`{"sut_id":"9001","asset_kind":"page","name":"x","url":"https://x.example.com","evidence_refs":["s"]}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeStateConflict {
		t.Fatalf("gate failure = %+v, want state_conflict", toolErr)
	}
}

// fakeSUTRegistrar is the additive R-B4 new-SUT registration port stub. It records
// the parsed candidate + injected scope so a test can pin the tool's six surfaces.
type fakeSUTRegistrar struct {
	in     SUTRegisterInput
	scope  Scope
	called int
	result any
	err    error
}

func (f *fakeSUTRegistrar) RegisterSUT(_ context.Context, scope Scope, in SUTRegisterInput) (any, error) {
	f.called++
	f.scope = scope
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return map[string]any{"sut_id": "9101", "status": "approved", "source": "ai", "approved": true}, nil
}

// TestSUTRegisterTool pins the sut.register tool (SC-F4): it is registered only when
// the new-SUT registration port is wired (never half-wired), decodes the candidate,
// and forwards the parsed input + injected scope to the server-side gate. The
// Description names the sha256 evidence gate and the match-existing-first rule.
func TestSUTRegisterTool(t *testing.T) {
	reg := &fakeSUTRegistrar{}
	registry := NewKnowledgeRegistry(Deps{SUTRegistration: SUTRegistrationDeps{Registrar: reg}})
	tool, ok := registry.Get(ToolSUTRegister)
	if !ok {
		t.Fatalf("NewKnowledgeRegistry is missing %q when the registration port is wired", ToolSUTRegister)
	}
	if !strings.Contains(tool.Description(), "sha256") {
		t.Fatalf("sut.register description must name the sha256 evidence gate: %s", tool.Description())
	}
	// Runtime cleanliness (AGENTS §2.2): the prompt-rendering Description must
	// not leak development-stage codenames or governance decision ids.
	for _, codename := range []string{"R-B4", "R-F1", "GD-01", "P17", "SC-F"} {
		if strings.Contains(tool.Description(), codename) {
			t.Fatalf("sut.register description leaks the codename %q: %s", codename, tool.Description())
		}
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolSUTRegister); ok {
		t.Fatal("sut.register must stay unregistered without a registration port")
	}

	result, err := registry.Execute(context.Background(),
		Scope{VersionID: 2001, CaseRunID: 5001, AgentRunID: 3001}, ToolSUTRegister,
		json.RawMessage(`{"name":"订单中心","intro":"orders","base_url":"https://orders.example.com","family":"orders","evidence_refs":["sha-proof"]}`))
	if err != nil {
		t.Fatalf("sut.register: %v", err)
	}
	if reg.called != 1 {
		t.Fatalf("registrar called %d times, want 1", reg.called)
	}
	if reg.in.Name != "订单中心" || reg.in.BaseURL != "https://orders.example.com" || reg.in.Family != "orders" {
		t.Fatalf("parsed candidate = %+v", reg.in)
	}
	if len(reg.in.EvidenceRefs) != 1 || reg.in.EvidenceRefs[0] != "sha-proof" {
		t.Fatalf("evidence_refs = %v, want [sha-proof]", reg.in.EvidenceRefs)
	}
	if reg.scope.VersionID != 2001 || reg.scope.CaseRunID != 5001 || reg.scope.AgentRunID != 3001 {
		t.Fatalf("injected scope = %+v, want version 2001 case_run 5001 run 3001", reg.scope)
	}
	body := resultMap(t, result)
	if body["status"] != "approved" || body["sut_id"] != "9101" {
		t.Fatalf("result = %#v, want approved SUT 9101", body)
	}
}

// TestSUTRegisterRejectsBadArgs: empty name / base_url / evidence_refs are
// model-fixable retryable argument errors carrying the top-level field name, and
// never reach the registration gate.
func TestSUTRegisterRejectsBadArgs(t *testing.T) {
	cases := []struct{ payload, field string }{
		{`{"name":"","base_url":"https://x.example.com","evidence_refs":["s"]}`, "name"},
		{`{"name":"x","base_url":"","evidence_refs":["s"]}`, "base_url"},
		{`{"name":"x","base_url":"https://x.example.com","evidence_refs":[]}`, "evidence_refs"},
		{`{"name":"x","base_url":"https://x.example.com"}`, "evidence_refs"},
	}
	for _, tc := range cases {
		t.Run(tc.field+"/"+tc.payload, func(t *testing.T) {
			reg := &fakeSUTRegistrar{}
			registry := NewKnowledgeRegistry(Deps{SUTRegistration: SUTRegistrationDeps{Registrar: reg}})
			_, err := registry.Execute(context.Background(), Scope{VersionID: 2001, CaseRunID: 5001},
				ToolSUTRegister, json.RawMessage(tc.payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != tc.field {
				t.Fatalf("field = %q, want %q", toolErr.Field, tc.field)
			}
			if reg.called != 0 {
				t.Fatal("a malformed argument reached the registration gate")
			}
		})
	}
}
