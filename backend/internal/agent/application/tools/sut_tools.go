package tools

import (
	"context"
	"encoding/json"
	"strings"
)

// ToolSUTRead is declared in registry.go (the additive P16 tool name).

// SUTDetail is one SUT of the version's bound environment in full, as the
// execution team reads it through sut.read (v0.2.4-F P16, SC-F2): the identity
// and registration labels (name/intro/base_url/source/status) plus the EFFECTIVE
// asset summary (approved pages/APIs, active databases). The production adapter
// derives it from versionapp.EnvironmentSUTCatalog.EffectiveSUTs, so a proposed/
// rejected SUT — or a proposed page of an approved SUT — is invisible here exactly
// as in sut.list/sut.page_list (R-B4 effective-scope gate). Credentials are never
// carried (the database summary is the read-only DatabaseSummary projection).
type SUTDetail struct {
	SUTID         int64
	Name          string
	Intro         string
	BaseURL       string
	Source        string
	Status        string
	Environment   string
	EnvironmentID int64
	Family        string
	Pages         []ResourcePage
	APIs          []ResourcePage
	Databases     []DatabaseSummary
}

// SUTReader is the additive read surface behind sut.read. The version adapter
// implements it. It is a separate interface (not a ResourcePort method) so
// existing resource ports and test fakes keep compiling; a port that does not
// implement it simply does not expose the tool (the DatabaseLister precedent).
type SUTReader interface {
	ReadSUT(ctx context.Context, versionID, sutID int64) (SUTDetail, error)
}

type sutReadParams struct {
	SUTID string `json:"sut_id"`
}

type sutRead struct{ deps Deps }

func (t *sutRead) Name() string { return ToolSUTRead }

func (t *sutRead) Description() string {
	return "读取当前版本绑定环境中某个 SUT 的详情（只读）：name/intro/base_url/source/status 与有效资产摘要（已批准页面 pages、已批准接口 apis、可用数据库 databases）。" +
		"执行团队解析用例 sut_description 时，先用 sut.list 看环境目录，再用本工具读候选 SUT 的 base_url/资产以确认匹配；命中后据此验证资产有效性。" +
		"sut_id 必填（十进制字符串，取自 sut.list）；不在有效范围内的 SUT 返回 not_found。资产 id 供执行定位，不用于需求/用例绑定。"
}

func (t *sutRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "SUT ID（十进制字符串，取自 sut.list）。",
			"pattern":     `^[1-9][0-9]*$`,
		},
	}, "sut_id")
}

func (t *sutRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutReadParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	sutID, toolErr := parsePositiveID("sut_id", params.SUTID)
	if toolErr != nil {
		return nil, retryableParamError(toolErr)
	}
	reader, ok := t.deps.Resource.(SUTReader)
	if !ok {
		return nil, missingDep("sut resource")
	}
	detail, err := reader.ReadSUT(ctx, scope.VersionID, sutID)
	if err != nil {
		return nil, mapPortError(err)
	}
	pages := make([]map[string]any, 0, len(detail.Pages))
	for _, page := range detail.Pages {
		pages = append(pages, map[string]any{
			"id":   page.ID,
			"name": page.Name,
			"path": page.Path,
		})
	}
	apis := make([]map[string]any, 0, len(detail.APIs))
	for _, api := range detail.APIs {
		apis = append(apis, map[string]any{
			"id":     api.ID,
			"name":   api.Name,
			"path":   api.Path,
			"method": api.Method,
		})
	}
	databases := make([]map[string]any, 0, len(detail.Databases))
	for _, database := range detail.Databases {
		databases = append(databases, map[string]any{
			"id":        database.ID,
			"name":      database.Name,
			"kind":      database.Kind,
			"read_only": database.ReadOnly,
		})
	}
	result := map[string]any{
		"version_id":     idString(scope.VersionID),
		"sut_id":         idString(detail.SUTID),
		"name":           detail.Name,
		"intro":          detail.Intro,
		"base_url":       detail.BaseURL,
		"source":         detail.Source,
		"status":         detail.Status,
		"environment":    detail.Environment,
		"environment_id": idString(detail.EnvironmentID),
		"pages":          pages,
		"apis":           apis,
		"databases":      databases,
		"asset_counts": map[string]any{
			"pages":     len(pages),
			"apis":      len(apis),
			"databases": len(databases),
		},
	}
	if detail.Family != "" {
		result["family"] = detail.Family
	}
	return result, nil
}

// init registers the additive SUT detail tool through the P4 tool-group mechanism
// (the sut.database_list precedent). A resource port without the SUTReader surface
// leaves sut.read unregistered instead of failing at call time.
//
// Capability three-list (constitution Constraints B.1): sut.read — Enabled
// (read-only effective-scope detail). Constrained: none. Forbidden: none (GD-01).
// The case_executor profile whitelist increment that exposes this tool to the
// executor lives in execution_profiles.go (the execution prompt/profile surface).
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.Resource == nil {
			return nil
		}
		if _, ok := deps.Resource.(SUTReader); !ok {
			return nil
		}
		return []Tool{&sutRead{deps: deps}}
	})
}

// --- P17 sut.asset_register (v0.2.4-F T10, SC-F3) ---------------------------

// AssetRegisterInput is the parsed candidate the sut.asset_register tool submits
// to the server-side evidence gate: the resolved SUT, the discovered asset kind
// and descriptor, and the content-addressed evidence refs that prove a step used
// the asset and its assertion passed. The model only proposes; the gate decides.
type AssetRegisterInput struct {
	SUTID        int64
	AssetKind    string
	Name         string
	URL          string
	Path         string
	Method       string
	DSN          string
	Username     string
	Password     string
	ReadOnly     *bool
	EvidenceRefs []string
}

// AssetRegistrar is the additive P17 registration surface. The execution
// AssetRegistrationService implements it (the server-side evidence gate). It is a
// separate interface so existing ports/fakes keep compiling; a zero Registrar
// leaves sut.asset_register unregistered (the SkillDeps precedent).
type AssetRegistrar interface {
	RegisterAsset(ctx context.Context, scope Scope, in AssetRegisterInput) (any, error)
}

// AssetRegistrationDeps is the per-deployment wiring of the P17 registration tool.
// A nil Registrar leaves the tool unregistered (never half-wired).
type AssetRegistrationDeps struct {
	Registrar AssetRegistrar
}

type sutAssetRegisterParams struct {
	SUTID        string   `json:"sut_id"`
	AssetKind    string   `json:"asset_kind"`
	Name         string   `json:"name"`
	URL          string   `json:"url,omitempty"`
	Path         string   `json:"path,omitempty"`
	Method       string   `json:"method,omitempty"`
	DSN          string   `json:"dsn,omitempty"`
	Username     string   `json:"username,omitempty"`
	Password     string   `json:"password,omitempty"`
	ReadOnly     *bool    `json:"read_only,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type sutAssetRegister struct{ deps Deps }

func (t *sutAssetRegister) Name() string { return ToolSUTAssetRegister }

func (t *sutAssetRegister) Description() string {
	return "把探索发现并经有效证明的资产（page/api/db）录入当前 SUT 的目录。" +
		"提交资产描述 + 证据引用（evidence_refs：内容寻址 sha256，取自 execution.evidence / execution.evidence_read）。" +
		"服务端证据门校验：证据存在 + sha256 与内容一致 + 关联步骤断言通过（服务端 verdict）→ 自动录入（page/api 为 source=ai/status=approved，db 为 active；无需人工审批）。" +
		"未成功证明（无证据/sha256 不符/断言未通过）则不录入，返回 evidence_gate_failed。" +
		"已被管理员驳回的资产需要晚于驳回时间的新证据才能重新录入；同名数据库若 DSN/用户名不一致或已停用则返回冲突。" +
		"sut_id 必填（十进制字符串，取自 sut.list/sut.read 已解析的 SUT）；asset_kind=page 需 name+url，api 需 name+method+path，db 需 name+dsn+username。" +
		"录入丰富目录，下一 wave 据此产生新指纹。"
}

func (t *sutAssetRegister) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "已解析的 SUT ID（十进制字符串，取自 sut.list/sut.read）。",
			"pattern":     `^[1-9][0-9]*$`,
		},
		"asset_kind": map[string]any{
			"type":        "string",
			"description": "资产类型。",
			"enum":        []string{"page", "api", "db"},
		},
		"name": map[string]any{"type": "string", "description": "资产名称。"},
		"url":  map[string]any{"type": "string", "description": "page 的 URL。"},
		"path": map[string]any{"type": "string", "description": "api 的 path template。"},
		"method": map[string]any{
			"type": "string", "description": "api 的 HTTP 方法。",
			"enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		},
		"dsn":       map[string]any{"type": "string", "description": "db 的 postgres DSN。"},
		"username":  map[string]any{"type": "string", "description": "db 用户名。"},
		"password":  map[string]any{"type": "string", "description": "db 口令（明文 by design，GD-01）。"},
		"read_only": map[string]any{"type": "boolean", "description": "db 是否只读（默认 true）。"},
		"evidence_refs": map[string]any{
			"type":        "array",
			"description": "证明该资产被成功使用且断言通过的内容寻址证据 sha256（取自 execution.evidence / execution.evidence_read）。",
			"items":       map[string]any{"type": "string"},
			"minItems":    1,
		},
	}, "sut_id", "asset_kind", "name", "evidence_refs")
}

func (t *sutAssetRegister) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutAssetRegisterParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	sutID, toolErr := parsePositiveID("sut_id", params.SUTID)
	if toolErr != nil {
		return nil, retryableParamError(toolErr)
	}
	switch params.AssetKind {
	case "page", "api", "db":
	default:
		return nil, InvalidWithAllowed("asset_kind", "must be one of page, api, db", []string{"page", "api", "db"}, "choose the discovered asset kind")
	}
	if len(params.EvidenceRefs) == 0 {
		return nil, InvalidField("evidence_refs", "must cite at least one evidence sha256 proving the asset")
	}
	registrar := t.deps.AssetRegistration.Registrar
	if registrar == nil {
		return nil, missingDep("asset registration")
	}
	in := AssetRegisterInput{
		SUTID:        sutID,
		AssetKind:    params.AssetKind,
		Name:         params.Name,
		URL:          params.URL,
		Path:         params.Path,
		Method:       params.Method,
		DSN:          params.DSN,
		Username:     params.Username,
		Password:     params.Password,
		ReadOnly:     params.ReadOnly,
		EvidenceRefs: params.EvidenceRefs,
	}
	result, err := registrar.RegisterAsset(ctx, scope, in)
	if err != nil {
		return nil, mapPortError(err)
	}
	return result, nil
}

// sutAssetRegisterDomainTools builds the P17 registration tool. The admin catalog
// adds it with nil deps (metadata only; Execute fails closed).
//
// Capability three-list (constitution Constraints B.1): sut.asset_register —
// Enabled (register a proven discovery; server evidence gate, auto-approved on
// proof). Constrained: none. Forbidden: none (GD-01; an evidence gate, not an
// approval gate). The case_executor profile whitelist increment lives in
// execution_profiles.go (T15, the execution prompt/profile surface).
func sutAssetRegisterDomainTools(deps Deps) []Tool {
	return []Tool{&sutAssetRegister{deps: deps}}
}

// init registers the P17 tool through the additive P4 tool-group mechanism: an
// unwired registrar leaves sut.asset_register unregistered instead of failing at
// call time (the skill domain precedent).
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.AssetRegistration.Registrar == nil {
			return nil
		}
		return sutAssetRegisterDomainTools(deps)
	})
}

// --- R-B4 sut.register (v0.2.4-F T11, SC-F4) --------------------------------

// SUTRegisterInput is the parsed candidate the sut.register tool submits to the
// server-side evidence gate: the new SUT descriptor (name/intro/base_url/family) and
// the content-addressed evidence refs that prove the agent reached and validated a
// resource matching no existing SUT. The model only proposes; the gate decides
// whether it is auto-approved (R-F1) or stays proposed for admin audit.
type SUTRegisterInput struct {
	Name         string
	Intro        string
	BaseURL      string
	Family       string
	EvidenceRefs []string
}

// SUTRegistrar is the additive R-B4 new-SUT registration surface. The execution
// SUTRegistrationService implements it (the server-side evidence gate). It is a
// separate interface so existing ports/fakes keep compiling; a nil Registrar leaves
// sut.register unregistered (the AssetRegistrar precedent).
type SUTRegistrar interface {
	RegisterSUT(ctx context.Context, scope Scope, in SUTRegisterInput) (any, error)
}

// SUTRegistrationDeps is the per-deployment wiring of the R-B4 registration tool. A
// nil Registrar leaves the tool unregistered (never half-wired).
type SUTRegistrationDeps struct {
	Registrar SUTRegistrar
}

type sutRegisterParams struct {
	Name         string   `json:"name"`
	Intro        string   `json:"intro,omitempty"`
	BaseURL      string   `json:"base_url"`
	Family       string   `json:"family,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type sutRegister struct{ deps Deps }

func (t *sutRegister) Name() string { return ToolSUTRegister }

func (t *sutRegister) Description() string {
	return "把探索发现、匹配不到任何已有 SUT 的新系统上报为当前版本绑定环境的新 SUT（探索→证明→注册）。" +
		"先用 sut.list/sut.read 匹配已有 SUT；确认无匹配才用本工具新建（先匹配已有，无匹配才新建）。" +
		"提交 SUT 描述（name/base_url 必填，intro/family 可选）+ 证据引用（evidence_refs：内容寻址 sha256，取自 execution.evidence / execution.evidence_read）。" +
		"服务端证据门校验：证据存在 + sha256 与内容一致 + 关联步骤断言通过（服务端 verdict）→ 自动以 source=ai/status=approved 转正（无需人工审批）。" +
		"未成功证明则记为 source=ai/status=proposed（不进入有效范围，待管理员事后审计/驳回）。" +
		"与已被管理员驳回的 SUT 同域名的新 SUT，需要晚于驳回时间的新证据才能自动转正，否则记为待管理员审计。" +
		"匹配判据 base_url/域名 + 语义为主、父路径同源辅助。新 SUT 批准后进入有效范围，下一 wave 据此产生新指纹。"
}

func (t *sutRegister) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"name":     map[string]any{"type": "string", "description": "新 SUT 名称（环境内唯一）。"},
		"intro":    map[string]any{"type": "string", "description": "SUT 简介（供语义匹配）。"},
		"base_url": map[string]any{"type": "string", "description": "SUT 入口 base_url（绝对 http(s) URL）。"},
		"family":   map[string]any{"type": "string", "description": "可选 SUT family slug（小写，无空白）。"},
		"evidence_refs": map[string]any{
			"type":        "array",
			"description": "证明该新 SUT 被成功触达且断言通过的内容寻址证据 sha256（取自 execution.evidence / execution.evidence_read）。",
			"items":       map[string]any{"type": "string"},
			"minItems":    1,
		},
	}, "name", "base_url", "evidence_refs")
}

func (t *sutRegister) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params sutRegisterParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, retryableParamError(err)
	}
	if strings.TrimSpace(params.Name) == "" {
		return nil, InvalidField("name", "must not be empty")
	}
	if strings.TrimSpace(params.BaseURL) == "" {
		return nil, InvalidField("base_url", "must not be empty")
	}
	if len(params.EvidenceRefs) == 0 {
		return nil, InvalidField("evidence_refs", "must cite at least one evidence sha256 proving the new SUT")
	}
	registrar := t.deps.SUTRegistration.Registrar
	if registrar == nil {
		return nil, missingDep("sut registration")
	}
	in := SUTRegisterInput{
		Name:         params.Name,
		Intro:        params.Intro,
		BaseURL:      params.BaseURL,
		Family:       params.Family,
		EvidenceRefs: params.EvidenceRefs,
	}
	result, err := registrar.RegisterSUT(ctx, scope, in)
	if err != nil {
		return nil, mapPortError(err)
	}
	return result, nil
}

// sutRegisterDomainTools builds the R-B4 registration tool. The admin catalog adds
// it with nil deps (metadata only; Execute fails closed).
//
// Capability three-list (constitution Constraints B.1): sut.register — Enabled
// (register a proven new SUT; server evidence gate, auto-approved on proof).
// Constrained: none. Forbidden: none (GD-01; an evidence gate, not an approval gate).
// The case_executor profile whitelist increment lives in execution_profiles.go (T15).
func sutRegisterDomainTools(deps Deps) []Tool {
	return []Tool{&sutRegister{deps: deps}}
}

// init registers the R-B4 tool through the additive P4 tool-group mechanism: an
// unwired registrar leaves sut.register unregistered instead of failing at call time.
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.SUTRegistration.Registrar == nil {
			return nil
		}
		return sutRegisterDomainTools(deps)
	})
}
