package tools

import (
	"context"
	"encoding/json"
)

// P18 session fixture tools (v0.2.4-F, SC-F5; design v1.3.1 §6.4). The fixture
// capability domain makes a proven login state reusable across case_runs:
//
//	fixture.upload   — after browser.cli state-save, store the workspace state
//	                   file in the server fixture library (append-only fact,
//	                   content-addressed bytes, reuse key version/sut/env/role).
//	fixture.download — in a later case_run, fetch the current fixture of the
//	                   reuse key and inject it via CLI state-load.
//
// The model only chooses WHAT to save/reuse; the server derives the reuse key's
// environment from the effective SUT catalog, validates the state file, stores
// the bytes content-addressed and records the reuse link. A failed injection is
// a typed error (re-login / re-fetch), never a silent stale login state.

// FixtureUploadInput is the parsed fixture.upload candidate: which (SUT,
// account_role) session saved the state, and the workspace path of the CLI
// state-save output (empty = the conventional state.json).
type FixtureUploadInput struct {
	SUTID       int64
	AccountRole string
	Path        string
}

// FixtureDownloadInput is the parsed fixture.download request: which (SUT,
// account_role) session injects the fixture, and the workspace path to write it
// to (empty = fixture-state.json, deliberately distinct from the state-save
// file so an injection never clobbers a freshly saved state).
type FixtureDownloadInput struct {
	SUTID       int64
	AccountRole string
	Path        string
}

// FixtureLibrary is the additive P18 server surface behind the fixture tools.
// The execution SessionFixtureService implements it. It is a separate interface
// so existing ports/fakes keep compiling; a nil Library leaves both tools
// unregistered (the AssetRegistrar precedent).
type FixtureLibrary interface {
	UploadFixture(ctx context.Context, scope Scope, in FixtureUploadInput) (any, error)
	DownloadFixture(ctx context.Context, scope Scope, in FixtureDownloadInput) (any, error)
}

// SessionFixtureDeps is the per-deployment wiring of the P18 fixture tools. A
// nil Library leaves the tools unregistered (never half-wired).
type SessionFixtureDeps struct {
	Library FixtureLibrary
}

type fixtureUploadParams struct {
	SUTID       string `json:"sut_id"`
	AccountRole string `json:"account_role,omitempty"`
	Path        string `json:"path,omitempty"`
}

type fixtureUploadTool struct{ deps Deps }

func (t *fixtureUploadTool) Name() string { return ToolFixtureUpload }

func (t *fixtureUploadTool) Description() string {
	return "把当前会话已保存的登录态存入服务端会话夹具库，供同版本后续用例复用（跨用例会话复用层）。" +
		"先用 browser.cli state-save 把登录态保存为工作区文件（默认 state.json），再调用本工具上传该文件。" +
		"服务端按 (version, sut, environment, role) 复用键追加一条不可变夹具事实（内容寻址 sha256），并在 case_run_sut_context 记录夹具引用；重复保存追加新版本，最新生效。" +
		"sut_id 必填（十进制字符串，取自 sut.list/sut.read 已解析的 SUT）；account_role 选择会话角色（缺省为默认会话）；path 为 state-save 写出的工作区相对路径（缺省 state.json）。" +
		"文件必须是浏览器 storage state JSON（含 cookies/origins），否则拒绝。遇到 2FA 等人工验证可先 human.request(login_takeover) 暂停，人工完成后再保存。"
}

func (t *fixtureUploadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "已解析的 SUT ID（十进制字符串，取自 sut.list/sut.read）。",
			"pattern":     `^[1-9][0-9]*$`,
		},
		"account_role": map[string]any{
			"type":        "string",
			"description": "会话的账号角色（每个角色独占一个会话/工作区）；缺省为默认会话。",
		},
		"path": map[string]any{
			"type":        "string",
			"description": "state-save 写出的工作区相对文件路径（缺省 state.json）；不允许绝对路径或 .. 越界。",
		},
	}, "sut_id")
}

func (t *fixtureUploadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params fixtureUploadParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	sutID, toolErr := parsePositiveID("sut_id", params.SUTID)
	if toolErr != nil {
		return nil, retryableParamError(toolErr)
	}
	if err := validateText("account_role", params.AccountRole, 0, 64); err != nil {
		return nil, retryableParamError(err)
	}
	library := t.deps.SessionFixtures.Library
	if library == nil {
		return nil, missingDep("session fixture library")
	}
	result, err := library.UploadFixture(ctx, scope, FixtureUploadInput{
		SUTID:       sutID,
		AccountRole: params.AccountRole,
		Path:        params.Path,
	})
	if err != nil {
		return nil, mapPortError(err)
	}
	return result, nil
}

type fixtureDownloadParams struct {
	SUTID       string `json:"sut_id"`
	AccountRole string `json:"account_role,omitempty"`
	Path        string `json:"path,omitempty"`
}

type fixtureDownloadTool struct{ deps Deps }

func (t *fixtureDownloadTool) Name() string { return ToolFixtureDownload }

func (t *fixtureDownloadTool) Description() string {
	return "从服务端会话夹具库取出当前 (version, sut, environment, role) 复用键的最新登录态夹具，写入本会话工作区并经 browser.cli state-load 注入浏览器会话，实现跨用例登录态复用。" +
		"sut_id 必填（十进制字符串，取自 sut.list/sut.read 已解析的 SUT）；account_role 选择会话角色（缺省为默认会话）；path 为注入用的工作区相对路径（缺省 fixture-state.json，与 state-save 文件分开）。" +
		"无夹具返回 not_found：请先自主登录（browser.cli + account.credentials），再 state-save + fixture.upload 保存。" +
		"夹具失效/加载失败返回 fixture_load_failed 或 fixture_content_unavailable：请重新登录并重新保存夹具（或重试下载），不要当作已登录继续执行。" +
		"注入成功后建议 snapshot 验证登录态仍然有效；服务端会在 case_run_sut_context 记录本次复用的夹具引用。"
}

func (t *fixtureDownloadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "已解析的 SUT ID（十进制字符串，取自 sut.list/sut.read）。",
			"pattern":     `^[1-9][0-9]*$`,
		},
		"account_role": map[string]any{
			"type":        "string",
			"description": "会话的账号角色（每个角色独占一个会话/工作区）；缺省为默认会话。",
		},
		"path": map[string]any{
			"type":        "string",
			"description": "注入用的工作区相对文件路径（缺省 fixture-state.json）；不允许绝对路径或 .. 越界。",
		},
	}, "sut_id")
}

func (t *fixtureDownloadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params fixtureDownloadParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	sutID, toolErr := parsePositiveID("sut_id", params.SUTID)
	if toolErr != nil {
		return nil, retryableParamError(toolErr)
	}
	if err := validateText("account_role", params.AccountRole, 0, 64); err != nil {
		return nil, retryableParamError(err)
	}
	library := t.deps.SessionFixtures.Library
	if library == nil {
		return nil, missingDep("session fixture library")
	}
	result, err := library.DownloadFixture(ctx, scope, FixtureDownloadInput{
		SUTID:       sutID,
		AccountRole: params.AccountRole,
		Path:        params.Path,
	})
	if err != nil {
		return nil, mapPortError(err)
	}
	return result, nil
}

// fixtureDomainTools builds the P18 fixture capability domain. The admin catalog
// adds them with nil deps (metadata only; Execute fails closed).
//
// Capability three-list (constitution Constraints B.1): fixture.upload /
// fixture.download — Enabled (save/reuse a login state; server-truth reuse key
// and content addressing). Constrained: none. Forbidden: none (GD-01). The
// case_executor profile whitelist increment lives in execution_profiles.go (T15, the
// execution prompt/profile surface).
func fixtureDomainTools(deps Deps) []Tool {
	return []Tool{&fixtureUploadTool{deps: deps}, &fixtureDownloadTool{deps: deps}}
}

// init registers the P18 domain through the additive P4 tool-group mechanism:
// an unwired library leaves the tools unregistered instead of failing at call
// time (the skill domain precedent).
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.SessionFixtures.Library == nil {
			return nil
		}
		return fixtureDomainTools(deps)
	})
}
