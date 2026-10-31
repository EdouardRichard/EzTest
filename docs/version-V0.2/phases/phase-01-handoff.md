# Phase 01 Handoff — 控制面 + 澄清

> 状态：completed
> 日期：2026-10-31
> 阶段：P1（`phase-01-runtime-clarification.md`）
> 实施分支：`feat/v0.2-p1`（从 `feat/v0.2-p0` @0ef7c92 拉出）
> 实施 spec：[2026-10-31-eztest-v02-p1-runtime-clarification-design.md](../../superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md)
> 实施 plan：[2026-10-31-eztest-v02-p1-runtime-clarification.md](../../superpowers/plans/2026-10-31-eztest-v02-p1-runtime-clarification.md)

## 0. 启动提示词映射说明

本会话顶部提示词的读取清单误用了 P0 模板（`phase-00-platform-foundation.md` + “无前置 handoff”），但 `implementation-phase-plan.md` 的阶段复制表明确规定 `P1 → phase-01-runtime-clarification.md`、`PREV_HANDOFF=phase-00-handoff.md`，且仓库 P0 已完成并冻结。本阶段按 **P1 = phase-01** 实施，未重做 P0；未修改 `docs/version-V0.2` 设计文档。

## 1. 完成范围

- [x] 严格 4 项 `EZTEST_*` env；P1 不新增 env/设置键；`llm_*` 仅验收脚本读取，经 admin API 写入 DB `llm_config`。
- [x] 迁移：`agent_0001_work_item`、`agent_0002_team_room`、`agent_0003_runtime`、`version_0002_requirement`、`version_0003_conversation`、`version_0004_requirement_attachment`、`eventing_0002_event_index`；顺序追加到 `order.txt`；集成测试重复执行 no-op。
- [x] 任务板/租约/fencing：`work_item` 7 态（P1 只写 6 态 + dead，不写 waiting_human）、`FOR UPDATE SKIP LOCKED` 领取、30s lease/10s heartbeat、`lease_epoch` CAS、重试退避 `min(2^n,30s)`、dead、过期恢复、20 并发领取无重复。
- [x] Reconciler（3s）：版本 `initial→running` 投影、opening/reply/curate/attachment ingest 的幂等 ensure；重复扫描不新增任务；不批量中断 running。
- [x] Agent runtime：3 个 profile（`clarification_host`/`requirement_curator`/`context_scout`）幂等 seed；14 个 P1 工具（schema 无 `version_id`，scope 注入）；run loop（context packet、tool_calls、max turns、soft/hard timeout、overflow 压缩重试、工具结果 >32KiB 落盘、LeaseLost 不覆盖）；Host 消息/Curator requirement_card/Scout finding。
- [x] 上下文：模型 `context_window/max_output` 优先于设置；缺失回退 `context.window_tokens/output_reserve_tokens`；滚动摘要 + pinned facts + 最近窗口；`context.compacted` 非失败终态；context packet hash/token 持久化。
- [x] 团队频道：`team_room_post/digest`、20 条/10 分钟摘要、保留最近 5 版、只读 `GET /api/versions/{id}/team-rooms/{team}`；前端折叠面板。
- [x] 需求：不可变版本、draft/clarified/closed/obsolete、单条/批量确认（事务 all-or-nothing + 已确认幂等跳过）、回退新 revision、关系提议、版本投影、统计；AI 不能确认/删除/关闭。
- [x] 对话：conversation/message/summary、`client_message_id` 幂等、opening/reply/curate 任务、SSE `version:{id}`、Markdown/附件/回退卡/需求内联卡。
- [x] 附件：md/txt/docx/pptx/pdf 文本提取；图片仅引用；旧 `.doc/.ppt` 明确失败；提取文本入 content ledger；失败不阻塞消息（POST 仍 201）；`.data` 路径不泄露。
- [x] 前端：需求与澄清 Tab（ChatPanel + RequirementSidebar/Editor/BulkConfirm/RollbackCard/DesignCTA/TeamRoomPanel）、URL 深链、SSE、响应式、真实阶段占位文案。
- [x] 契约：`openapi.yaml`（53 paths/81 schemas，P1 path item 10 个）、`events.md`（23 P1 事件）、`db-schema.md`（16 表）、`migrations-order.md`、`task-types.md`、`agent-profiles-tools.md`、`settings.md` 冻结为 `frozen: P1`。
- [x] P0 漂移收口：LLM list/status 契约按实现修正；event `(channel,created_at)` 索引；SSE `X-Snapshot-Cursor`；`Event.seq/snapshot_cursor` 改为整数（D4 用户裁决）；`version_sut.scope` DB 数字/API 字符串说明。
- [x] fake LLM 兼容 P0 echo，并支持 Host/Curator/Scout、overflow 注入；真实 DeepSeek tool name sanitize（点号→下划线，响应映射回点号）。

## 2. 契约变更

| 类型 | 变更 | frozen |
|---|---|---|
| API | P1 conversations/messages/requirements/confirmations/rollback/relations/team-rooms；版本详情增量 counts | `frozen: P1` |
| 事件 | 23 个 P1 事件；频道仍 `system`/`version:{id}`；scope 带 conversation/requirement/agent_run/work_item/attachment ID | `frozen: P1` |
| 表 | agent 8 表 + version 8 表 + eventing 索引 | `frozen: P1` |
| 迁移 | 7 个增量迁移（见下） | `frozen: P1` |
| 任务/工具 | `chat.turn`、`requirement.curate`、`attachment.ingest`；3 profiles；14 tools | `frozen: P1` |
| 设置 | 不新增键；新增只读 `settingsapp.Float` | `frozen: P1` |

迁移顺序（`order.txt` 追加）：

```text
agent_0001_work_item.sql
agent_0002_team_room.sql
agent_0003_runtime.sql
version_0002_requirement.sql
version_0003_conversation.sql
version_0004_requirement_attachment.sql
eventing_0002_event_index.sql
```

- 破坏性变更：无。P0 已应用迁移未修改；仅追加新索引。
- 用户裁决记录：附件支持现代 docx/pptx + 旧 `.doc/.ppt` 明确失败；`Event.seq` 修代码为整数；running 由首条消息/首个需求/首次确认触发；团队频道补只读 API+面板；Markdown 用 `react-markdown`；真实 LLM 用 `.env` `llm_*`（`deepseek-flash`）。

## 3. 测试与验收证据

| 证据 | 命令 | 结果/路径 |
|---|---|---|
| 全量门禁 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` | **EXIT=0**；Go 单测+集成全部 ok；前端 43 files / 341 tests；日志 `.tmp/p1/final-gate.log` |
| API 验收 | `powershell.exe -File scripts/p1-acceptance.ps1 -FakeLLMPort 13335` | **PASS（19 steps）**；隔离 `eztest_v02_e2e`；证据 `.tmp/p1-acceptance/api.log` |
| 浏览器 E2E | `powershell.exe -File scripts/p1-e2e.ps1` | **PASS**；登录→需求 Tab→新对话→附件→AI 草案→编辑→单条确认→批量确认→团队频道（断言 Scout 已读附件文本）→设计 CTA（断言 3 条待设计）；截图 `.tmp/p1-acceptance/screenshots/requirements-workspace.png`、`team-room.png`；日志 `e2e-playwright.log` |
| 真实 LLM | `powershell.exe -File scripts/p1-acceptance-llm.ps1` | **PASS**；DeepSeek `deepseek-flash` 真实回复+工具调用+draft+确认；工具名 dot→underscore 映射、脚本 UTF-8 body；证据 `.tmp/p1-acceptance/llm.log` |
| 生产构建 | `cd frontend; pnpm build` | 成功；12 routes（含 `/versions/[id]`） |
| 契约解析 | `node -e "yaml.parse(openapi.yaml)"` | YAML OK；53 paths / 81 schemas / `x-frozen: P1` |
| 迁移幂等 | `go test -tags=integration ./internal/db/...` | 15 migrations；第二次 applied 为空；P1 约束生效 |
| UTF-8 扫描 | strict scan `backend/**/*.go` + scripts | 0 invalid |

验收清单（phase-01）：
- [x] 新对话 → Host 读版本上下文/附件 → Host 回复 + Curator draft。
- [x] 编辑/单条/批量确认 → 状态/确认记录/事件正确；AI 回退卡（组件测试 + API 验收回退）→ 用户确认 → 新 draft revision + stale hook 调用（P1 空 affected）。
- [x] 并发领取不重复；租约过期恢复；旧 epoch 写回 409；重复扫描不重复任务。
- [x] 压缩保留 pinned/最近/任务约束；context_overflow 不判失败（单测 + 验收）。
- [x] 附件 md/txt/docx/pptx/pdf；损坏/不支持格式可解释失败；失败不阻塞消息。
- [x] 版本 initial→running；requirements Tab、SSE、团队面板、设计 CTA 可见。
- [x] 全量测试绿；E2E/真实 LLM 通过；契约冻结。

## 4. 已知缺口/偏差

| 缺口/偏差 | 影响 | 阻塞下一阶段 | 决策 |
|---|---|---|---|
| 旧二进制 `.doc/.ppt` 不解析 | 仅老格式附件失败（明确 `unsupported_attachment_format`） | 否 | 用户已裁决现代 docx/pptx 优先 |
| P1 stale 只到端口/事件 | 无可端到端用例 stale 表 | 否（P2 接 case 表） | P1-D12；事件 `affected_case_version_ids=[]` |
| 真实 LLM `/models` 元数据可能缺失 | 窗口回退 settings；本轮 DeepSeek catalog 返回了窗口元数据 | 否 | P1-D18 / Q23 |
| 真实模型质量只验证一条链路 | 不证明澄清质量/多轮稳定性 | 否（P8 深化） | 仅证明可运行路径 |
| P0 D6–D10 额外漂移 | auth 事件未发/未知字段/settings 边界/错误码清单/audit 顺序 | 否 | 登记 backlog，P8 收口 |
| 附件 content_ref 孤儿竞态 | 极端去重竞态下可能留 ref | 否 | 无删除 API；GC P9 |
| 需求列表只加载首个 100 条 | >100 需求需后续分页 UI | 否 | API 支持 before cursor |
| 回退卡仅组件测试 + API 验收 | 浏览器 E2E 未构造 rollback_proposal | 否 | 组件测试覆盖；P2 可补 AI 回退 E2E |
| 无通用 `Idempotency-Key` | 确认/消息使用唯一约束/自然幂等 | 否 | P0/P1 记录；后续接中间件 |
| 单实例 Hub/worker | 多副本实时 fan-out 未承诺 | 否（P7） | P1 单实例假设已写入包注释 |
| `react-markdown` 新依赖 | 前端构建体积增加 | 否 | 用户已批准 |

## 5. 下一阶段输入（P2 设计/用例）

必须读取/复用：
- `contracts/openapi.yaml` P1 部分：requirements/conversations 模型、`targets` 对象、`history`、确认/批量/回退；`contracts/events.md` P1 事件。
- 表：`requirement`/`requirement_version`/`requirement_confirmation`/`requirement_relation`、`conversation*`、`attachment`；`version` 的 running 投影与统计（`requirement_counts`/`conversation_count`/`pending_design_count`）。
- 端口：`versionapp.CaseStaleHook`（P2 实现真实 case stale）；`pending_design_count` 当前 = clarified 数，P2 接 `design_scope` 后替换。
- 任务/工具预留：`task-types.md` P2+ 类型；`agent-profiles-tools.md` reserved tools；Agent 端口（RequirementPort/ConversationPort/VersionReader/AttachmentReader）可扩展。
- 前端：`features/clarification` 的 API/types/组件模式、`useVersionEvents`、URL 深链、只读团队面板；P2 设计页独立 feature，不塞进 requirements Tab。

前置检查：
1. `scripts/test.ps1` 全绿；`tests` 无回归。
2. 不改 P1 frozen 契约；需要破坏性变更先停止并更新 contracts/决策/handoff。
3. 不提前建 P2 表；P1 的 `pending_design_count`/stale 占位按约定替换。
4. 真实 LLM 工具名映射对新增工具继续生效（dotted internal name → sanitized upstream）。

建议先做：
- P2 设计团队/脑图/用例版本/确认；把 P1 stale hook 接到 CaseVersion；design_scope 表与 CTA 数据源；复用 P1 task board/context/SSE/附件。

## 6. 风险

1. 资源/版本/需求契约已冻结，P2 若改字段需走破坏性变更流程；优先增量。
2. 上下文摘要质量/中文 token 估算保守；P8 要做质量与长会话回归。
3. 真实 LLM 工具调用/模型窗口依赖上游元数据；DeepSeek 工具名已做映射，新增供应商需回归 sanitize。
4. 单实例任务板/Hub；P7 前不要承诺多副本实时。
5. `react-markdown` 渲染安全依赖默认禁用 raw HTML；新增渲染特性需保持。
6. 附件解析覆盖现代格式，旧 `.doc/.ppt`/扫描 PDF 仍缺口；演示前确认素材格式。
7. P0 未清 dirty 文件（`docs/development.md`、`frontend/next-env.d.ts` 等）不是 P1 产物；提交时未纳入。
