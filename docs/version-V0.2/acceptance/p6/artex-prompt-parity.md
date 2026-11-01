# P6 ARTEX Prompt/Profile Capability Parity

> 用户裁决 A1（完整对齐核心，变量/预览按用户要求纳入；内网注入风险已接受）。
> 依据：`docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`agent-profiles-tools.md` §9、`.tmp/p6-research/08-artex-prompts.md`、`10-artex-agent-platform.md`。

## include（P6 已实现）

| 能力 | 0.2 实现 |
|---|---|
| 按 profile 编辑 system prompt，下一次 run 生效 | `PUT /api/agents/profiles/{key}/prompt`；`runner` 每 run 读 profile；`systemContent` 渲染 |
| append-only 版本 / 当前 / note / actor | `agent_profile_prompt_revision`（#33）；UNIQUE(profile_key,version)；`source∈seed|admin|reset` |
| 历史列表 / 查看 / 载入编辑器 | `GET .../prompt/revisions`；Agent 中心 `PromptVersionList` |
| 版本 diff | 前端行级 diff（`PromptVersionList`） |
| 恢复默认 / 版本恢复 | `POST .../prompt/reset`、`POST .../prompt/restore`（追加版本，不改历史） |
| 模板变量目录 / 白名单 | `agent/application/prompt_catalog.go`：8 通用 + 6 execution；保存校验未知变量 400 |
| Go template 渲染 / 回退 | `text/template` + `missingkey=error`；无 FuncMap；渲染失败回退 `DefaultPrompt`；`[profile:<key>]` 强制 |
| 渲染预览（示例/sample） | `POST .../prompt/preview` + 编辑器预览弹窗 |
| seed 仅空时 / 不覆盖管理员 | `EnsurePromptRevisions` v1（source=seed）；幂等 |
| profile model / budgets / allowed_tools | `PATCH /api/agents/profiles/{key}`；tools registry 校验；审计 |
| tools 目录 | `GET /api/agents/tools` |
| Agent 目录 / 运行观察 / 质量投影 | `/api/agent-center/{catalog,runs,quality}`；14 profile/6 team；不返回 prompt/transcript |
| admin-only 写 / 登录只读 / 脱敏 | admin guard；catalog 无 prompt；普通用户无编辑/配置 Tab |
| 审计（无正文） | `agent.profile.updated` metadata：profile_key/fields/prompt_version/prompt_sha256/prompt_length |
| 下一 run 语义 / 热生效 | profile 每 run 现读；进行中 run 快照不变；验收 S4 `context_packet` token |

## defer（0.2 当前模型不适用，后置）

| 能力 | 原因 |
|---|---|
| `enabled` 开关 | 0.2 runner 未消费；禁用后 work item 处置未定义，避免假开关 |
| profile 变更 SSE 事件 | 审计+读取已足够；新增事件需契约冻结 |
| profile name 编辑 | 无业务必要性；减少写面 |
| 跨 LLM 配置绑定 | 0.2 只支持模型名（默认 LLM 配置） |
| wrapup/收尾提示词（per-run/task-timeout） | 0.2 无 settlement 阶段；需新运行时阶段，不能只加字段 |
| effective system 预览（真实上下文） | 依赖任务/版本，可能泄露；P6 只做示例值预览 |
| token/费用估算 | UI 便利，非核心 |
| 自定义 agent 创建/删除/复制/导入导出 | 0.2 profile 由 work type/会话 kind 固定路由（14 个），新建 profile 无法被调度；属后续模型变更 |
| 触发器/run-merge/max_parallel | 0.2 是 work_item 租约队列，无触发器模型 |
| MCP/Skill 可见性 | 0.2 无对应运行时 |
| 工具描述/schema 覆盖 | 无工具表；代码 registry |
| web_search/interactive_shell | 无对应工具 |
| 多租户/按版本隔离 | 单组织；profile 全局 |
| transcript 暴露 | 0.1/0.2 脱敏红线，禁止 |

## 验收证据

- 真实 LLM next-run：S4 在 `clarification_host` prompt 加入唯一 token，启动真实澄清 run，`context_packet.sections` 含 token（2 条命中），run=done。
- 版本/预览/恢复/权限：S1/S4 API 断言；前端 25 tests；真实浏览器 E2E admin/普通用户截图。
- 详细差距与决策：`.tmp/p6-research/{08-artex-prompts,09-prompt-gap,10-artex-agent-platform}.md`。
