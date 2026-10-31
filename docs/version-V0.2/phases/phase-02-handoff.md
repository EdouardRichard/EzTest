# Phase 02 Handoff — 用例设计（脑图/确认/导出）

> 状态：completed
> 日期：2026-10-31
> 阶段：P2（`phase-02-design.md`）
> 实施分支：`feat/v0.2-p2`（从 `feat/v0.2-p1` 的 P1 完成点拉出）
> 实施 spec：[2026-10-31-eztest-v02-p2-case-design-design.md](../../superpowers/specs/2026-10-31-eztest-v02-p2-case-design-design.md)
> 实施 plan：[2026-10-31-eztest-v02-p2-case-design.md](../../superpowers/plans/2026-10-31-eztest-v02-p2-case-design.md)

## 0. 启动提示词映射说明

会话顶部提示词沿用了 P0 的读取清单，并声称“无前置 handoff”。实际 `implementation-phase-plan.md` 规定 P2 → `phases/phase-02-design.md`，前置为 P0/P1；P1 已完成并有 `phase-01-handoff.md`。本阶段按 **P2 = phase-02** 实施：

- 读取了 `Agent.md`、P2 设计文档、architecture/high-level/phase plan、全部 `contracts/`、`ui/mock.html`，以及 `docs/version-V0.2/phases/` 下全部阶段文件（含 P0/P1 handoff）。
- 未重做 P0/P1；未修改 `docs/version-V0.2` 设计文档。
- 用户参与 spec 评审并逐节批准；实现阶段按用户授权由 Lead 代为验收。用户裁决：表/字段/索引命名选 **A**（Agent.md §6.9/D34）、导出字节复用内容账本例外 **A**（P2-D-29）、LLM 测试 **A 双轨强化**（P2-D-30）。

## 1. 完成范围

- [x] design 上下文：`domain/application/infrastructure/interfaces/httpapi` 四层；9 张设计表 + `export_artifact`；迁移只追加 2 个文件。
- [x] 用例模型：scenario → case → immutable `case_version`（revision）→ `case_step`/`case_assertion`/`case_source`；确认表 `case_confirmation`。
- [x] 确认语义：case/scenario/requirement 三粒度；任意登录用户；整批事务；重复幂等；无 draft → 409；AI 工具不注册 `case.confirm`。
- [x] 图写：唯一写工具 `case.graph_propose`；自然键去重、内容 hash 幂等、每需求 advisory lock、可选 `base_graph_hash` 过期 409；AI 只能 draft。
- [x] 状态机：新 draft 创建即移动 `current_version_id`；新版本确认后旧 confirmed → superseded；stale → superseded 允许；需求改版重设计创建新场景/新用例，旧组保留 stale。
- [x] 跨 SUT 目标：`default_version_sut_id`、`version_sut_ids/page_ids/api_ids`；step 目标可覆盖；page/api/db 必须属于版本绑定（服务端校验）。
- [x] 断言五类 `ui/api/db/text/visual`；来源五类；P2 重点 requirement 来源。
- [x] 设计轮次/范围：`design_round` 每对话唯一；`design_scope` 唯一 `(version_id,requirement_id)`；D25：最新 design 对话永远活跃、不关闭；手工轮次激活全部 pending；已有设计对话时新 clarified 自动并入；`auto_design_new_requirements` 只在首条设计对话前自动建对话/轮次，不自动确认。
- [x] `pending_design_count` = clarified 且不在 active `design_scope`；版本投影 `initial→running` 在首条设计对话时触发。
- [x] Planner/reconciler：确定性扫描 + `design.round` LLM 范围说明；stale 安全网；critic/data_plan 任务派生；agent reconciler 每轮调用 design `Reconcile`（提交 `5c84400`）。
- [x] P1 stale 端口接真实用例：requirement `Update`/`Rollback` 均调用；`requirement.rolled_back`/`version_created` 的 `affected_case_version_ids` 填真实 ID。
- [x] 8 个 HTTP 端点：design graph/overview/case detail/confirmations；exports create/list/get/download（鉴权二进制 + sha 校验）。无关闭对话/轮次 API。
- [x] 导出：整版本当前 case 域、4 格式 Markdown/Mermaid/OPML/CSV；同步生成；每次新 revision；字节入 P0 content ledger，`content_ref(owner_kind=export)`；`paths.export_dir` 不使用。
- [x] Agent：5 个 P2 profiles（`design_host`/`design_planner`/`case_author`/`design_critic`/`data_planner`）、5 类任务、4 个工具；`designadapter` 连接 design 上下文；team-room 适配器。
- [x] fake LLM：设计对话/作者/批判/数据计划/故障注入确定性场景；真实模型用于验收。
- [x] 前端：版本「用例设计」Tab；上脑图 + 右聊天、下整行用例详情；React Flow 只读平移/缩放/适应；状态/优先级/目标 SUT 卡片；case/scenario/requirement 确认；导出格式弹窗 + 鉴权下载；自动设计开关；待设计 CTA；设计附件复用 P1；SSE 去抖快照刷新。
- [x] 真实 LLM 超时修复：移除 `http.Client` 全局 10s 超时，按 `llm_config.timeout_seconds` 为每次请求设置 context deadline；真实 DeepSeek 长 tool-call 响应不再被 10s 截断（`5041dd8`）。
- [x] 契约：`openapi.yaml`、`events.md`、`db-schema.md`、`migrations-order.md`、`task-types.md`、`agent-profiles-tools.md`、`settings.md` 冻结为 `x-frozen: P2` / `frozen: P2`；API 版本 `0.2.0-p2`。

## 2. 契约变更

| 类型 | 变更 | frozen |
|---|---|---|
| API | design graph/overview/case detail/confirmations；exports create/list/get/download；version `pending_design_count`/`case_counts`；conversation kind 允许 design | `x-frozen: P2` |
| 事件 | 9 个 `design.*` 事件；频道仍 `version:{id}`；P1 requirement 事件填真实 `affected_case_version_ids` | `frozen: P2` |
| 表 | design 9 表 + storage `export_artifact`；命名方案 A；PK/UK/IDX 显式 | `frozen: P2` |
| 迁移 | `design_0001_case_model.sql`、`storage_0002_export_artifact.sql`（P0/P1 15 个保持不变，共 17） | `frozen: P2` |
| 任务/工具 | 5 任务类型；5 profiles；`case.graph_propose`/`design.graph_read`/`design.review_submit`/`design.data_plan_submit`；不注册 `case.confirm` | `frozen: P2` |
| 设置 | 不新增设置键/env；`auto_design_new_requirements` 是 version 列；settings §4b 说明导出内容账本例外 | `frozen: P2` |

迁移顺序（`order.txt` 追加）：

```text
design_0001_case_model.sql
storage_0002_export_artifact.sql
```

- 破坏性变更：无。P0/P1 已应用迁移未修改；P2 只追加表/索引。
- 冻结检查：`node scripts/p2-contract-check.cjs` → `60 paths, 100 schemas, P2 increments present`。

## 3. 测试与验收证据

| 证据 | 命令 | 结果/路径 |
|---|---|---|
| 全量门禁 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` | **EXIT=0**；Go 单测+集成全部 ok；前端 54 files / 413 tests；日志 `.tmp/p2/final-gate.log` |
| P1 回归验收 | `powershell.exe -File scripts/p1-acceptance.ps1 -FakeLLMPort 13334` | **PASS（19 steps）**；API 版本 `0.2.0-p2`；日志 `.tmp/p1-acceptance/api.log` |
| P2 API 验收（fake） | `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334` | **PASS（21 steps）**；脑图、case/scenario/requirement 三粒度确认、幂等、Host 修改、stale/重设计、自动并入、附件、4 格式导出、SSE；日志 `.tmp/p2-acceptance/api.log` |
| P2 浏览器 E2E（fake） | `powershell.exe -File scripts/p2-e2e.ps1` | **1 passed**；登录→设计 Tab→新对话→脑图→详情→确认→Markdown 下载；`e2e-playwright.log`、`screenshots/` |
| 真实 LLM 探针 | `powershell.exe -File scripts/p2-acceptance-llm.ps1 -ProbeOnly` | **PASS**；DeepSeek `deepseek-flash` 约 18s 产出 2 条用例（首条 4 steps/2 assertions；真实模型输出有波动）；日志 `.tmp/p2-acceptance/llm-probe.log` |
| 真实 LLM API 验收 | `powershell.exe -File scripts/p2-acceptance-llm.ps1` | **PASS**；真实对话→实时图→修改图→按含 draft 的需求确认 2 版本→Markdown 导出 6503 chars；日志 `.tmp/p2-acceptance/llm.log` |
| 真实 LLM 浏览器 E2E | `powershell.exe -File scripts/p2-e2e-llm.ps1` | **1 passed（20.2s）**；真实模型主链路；日志 `.tmp/p2-acceptance/e2e-llm.log`、截图 `screenshots-llm/` |
| Server 集成 | `go test -tags=integration ./internal/server -run TestP2DesignRoutesAndEmptyGraph -v` | **PASS**；路由注册、空图、overview、export create/get/list、非法格式 400、未登录 401 |
| 契约解析 | `node scripts/p2-contract-check.cjs` | **OK**；60 paths / 100 schemas；P2 增量存在 |
| 迁移幂等 | `go test -tags=integration ./internal/db/...` | 17 migrations；P2 10 表；重复执行 applied 为空 |

验收清单（`phase-02-design.md`）：
- [x] 已澄清需求 → 新建设计对话 → 团队扫描 → 初版脑图实时渲染。
- [x] 对话修改 → 新增/修改 draft → 用户确认（单用例/场景/需求）。
- [x] 需求版本变化 → stale → 重设计；同一 case 的新 draft 确认后旧版本 superseded；需求改版产生的新 scenario/case 是新的 case，旧组保持 stale 历史，不跨 case supersede。旧 draft 保留历史。
- [x] 导出 4 格式可用；D25 开关/待设计 CTA 可验证。
- [x] 测试全绿；契约 frozen。

## 4. 关键实现位置

- 后端：`backend/internal/design/**`（domain/application/postgres/httpapi）；`backend/internal/storage/**`（export artifact + content ledger）；`backend/internal/agent/**`（profiles/tasks/tools/designadapter）；`backend/internal/version/**`（conversation design、stale hook、metrics、reconciler）；`backend/cmd/eztest/main.go`、`backend/internal/server/server.go`。
- 前端：`frontend/src/features/design/**`（API/types/DesignTab/DesignMindmap/CaseDetailPanel/DesignChat/ExportCasesDialog/AutoDesignSwitch/PendingDesignCTA）；`frontend/src/features/clarification/**` 的 SSE/附件/团队模式复用。
- 迁移：`backend/internal/db/migrations/design_0001_case_model.sql`、`storage_0002_export_artifact.sql`、`order.txt`。
- 验收脚本：`scripts/p2-acceptance.ps1`、`scripts/p2-e2e.ps1`、`scripts/p2-acceptance-llm.ps1`、`scripts/p2-e2e-llm.ps1`、`scripts/p2-contract-check.cjs`、`scripts/fake-llm.cjs`。
- 测试：`backend/internal/design/**/*_test.go`、`backend/internal/storage/**/*_test.go`、`backend/internal/version/**/*_test.go`、`backend/internal/server/server_integration_test.go`、`frontend/src/features/design/**/*.test.tsx`、`frontend/e2e/p2.spec.ts`。

## 5. 已知缺口/偏差

| 缺口/偏差 | 影响 | 阻塞下一阶段 | 决策/去向 |
|---|---|---|---|
| 真实 LLM 图质量/稳定性 | 严格校验下模型可能多次失败或语义波动；本次验收成功不证明批量质量 | 否 | P2-D-30；P8 质量评测/提示词迭代 |
| 图编辑并发 | 单实例 + 每需求 advisory lock；跨需求并行；多副本实时协作未实现 | 否（P7） | P2-D-17；单实例假设写入 spec |
| 最新设计对话永远活跃 | 版本一旦建过设计对话，后续 clarified 需求会持续自动并入；无法“结束本轮” | 否 | 用户裁决 P2-D-11/13/27；后续若需关闭走增量契约 |
| 导出字节复用内容账本 | `paths.export_dir` 未使用；P7 统一 GC 需消费 `content_ref` | 否 | P2-D-29 显式例外；P7 决策物理导出目录/GC |
| 设计附件 Scout 频道 | 复用 clarification ingest，Scout finding 落 clarification 频道 | 否 | P3/P6 可调整团队归属 |
| 大图渲染 | 前端目标 200 用例内；无虚拟化/截断 | 否 | P6/P8；布局测试覆盖 200 坐标 |
| `case.graph_propose` 校验严格 | 模型一次提交可能被拒；runtime 仅按 agent turn/retry 处理 | 否 | 验收用真实模型验证；后续可加结构化错误提示 |
| P2 无 `case.confirm` 模型工具 | AI 不能确认；确认只走用户 API | 否 | 架构/需求约束；不得为方便执行开放 |
| P1 LLM 客户端全局 10s 超时 | 已修复为按配置 timeout；真实验收用 300s | 否 | `5041dd8`；P1 回归验收 PASS |
| 导出 CSV 列语义/占位符 | CSV 的 `sut_names/page_names/api_targets` 当前为十进制 ID；Mermaid/OPML 未输出“未设计”占位和生成时间 | 否（P3 报告/导出可增强） | 已在评审记录为格式保真缺口；不改变 P2 冻结的 4 格式可用性 |
| Server 级 P2 E2E 替代 | `TestP2DesignRoutesAndEmptyGraph` 覆盖路由/空图/导出；完整用例流程由 `p2-acceptance.ps1` 覆盖 | 否 | 计划 T17 原意的显式替代；完整 server E2E 留 P3/P8 |
| requirement Update/Rollback stale hook 非同一事务 | 新 revision 已提交后 hook 失败会返回错误；reconciler stale 安全网可修复 | 否（单实例） | P2-D-16；P3 前如需强一致改为同事务 |
| planner 任务 enqueue 不在 version tx 内 | round/scope 已走同事务；work item ensure 仍走队列（幂等、reconciler 兜底） | 否 | 失败可重扫；不阻塞 P3 |
| graph_hash 覆盖范围 | 当前 hash 覆盖需求/场景 ID、名称/排序、case current/content hash；不覆盖 scenario description/version_sut_ids/page_ids/api_ids，纯场景元数据并发更新不会触发 base hash 409 | 否 | 评审 residual；P3 若需要再扩展 hash payload |
| propose 的 requirement status/current 检查在锁外 | 未传 base_graph_hash 时，并发需求更新仍可能生成旧版本 draft；P2 确认路径已拒绝旧版本，因此不会进入执行集 | 否 | 评审 residual；base hash 传入时锁内复检 |
| 断言显式 target 与步骤 target 强制相等 | 跨 SUT 断言若确需独立 target 会被拒绝 | 否（P2 语义选择） | 评审 residual；如 P3 需要跨 SUT 断言走增量契约 |
| 历史版本 draft 的确认按钮 | CaseCard/RequirementNode 对历史 draft 仍显示确认入口，点击后后端 400；CaseDetailPanel 已按 `confirmable` 隐藏 | 否（UX） | 评审 minor；P3 UI 可优化 |
| Agent 业务写回未按 lease_epoch CAS | 迟到 run 理论上可在接管窗口写图/确认 | 否（P1 单实例；P7 前必修） | P1 handoff 遗留风险；P7 |
| `Agent.md` §6.1 目录树未更新 | 文档树未列 design 上下文/文件 | 否 | 本阶段不做文档编辑；列入后续文档维护 |
| P1 长会话重复摘要 | 长设计对话可能重复摘要旧消息 | 否 | P1 遗留；P2/P8 长会话改进 |

## 5a. 独立评审与处置（task-6）

两次独立 read-only review（fresh reviewer + reviewer teammate）结论均为 **CHANGES REQUESTED**。已修复并复测：

- **Critical**：case-scope 确认未校验场景的 `requirement_version_id`，旧需求版本 draft 可被确认。已在 confirmation tx 内校验，并收紧 `confirmable`（selected == current、当前需求版本）；新增 `TestConfirmCaseRejectsOldRequirementVersionDraft` 与 `TestGetCaseConfirmableRequiresCurrentRequirementVersion`。
- **Important**：`base_graph_hash` 在锁外校验 → 在 requirement lock 内用 `CurrentGraphHash` 重新校验；scenario/case 累计上限 100/200 在事务内执行；确认 snapshot 的 step/assertion/source 计数改为 SQL 投影；data-plan revision 增加 requirement advisory lock；planner round/scope 写走同一 version tx；`sort_order`/`line_no` 契约漂移改为 `sort`/`line`；目标继承的 page/API/db 校验改为按 resolved target；工具增加 case/requirement scope 校验；scenario-scope 加入 P2 API 验收；真实探针使用独立 `llm-probe.log`；真实浏览器截图独立 `screenshots-llm/`。
- **Semantics**：`design_status` 只由当前 requirement version 的 case 驱动，历史 stale 不再压制当前 confirmed。
- **保留并记录**：CSV 名称列/占位符保真、server 级完整 P2 E2E 替代、stale hook 非同事务、planner 任务入队不在 version tx、P1 lease_epoch 遗留。见 §5。
- **复核**：聚焦 re-review 结论 **APPROVED FOR P2**（无 Critical/Important 阻塞）；仅保留 §5 的 minor/residual（graph_hash 场景元数据覆盖、锁外 status 检查、断言 target 过严、历史 draft 按钮 UX）。

复测证据：全量 gate EXIT=0；P1 回归 19 steps PASS；P2 API 21 steps PASS（含 scenario scope）；真实 LLM 探针/API/browser PASS；`node scripts/p2-contract-check.cjs` → 60 paths/100 schemas。

## 6. 下一阶段输入（P3 执行）

必须读取/复用：

- **用例数据**：`case_entity.current_version_id = case_version.id AND case_version.status='confirmed'` 才是可执行用例；`case_version` 不可变，执行不得原地改。
- **目标字段**：`case_step.target_version_sut_id/target_page_id/target_api_id`（空=继承 case 默认）、`case_assertion` 的 step/kind/target、`case_source`（requirement/code/page/api/knowledge）。
- **设计范围**：`design_scope`（version_id, requirement_id, active）用于覆盖判断；`design_round` 只读 latest design 对话的当前轮。
- **API**：`GET /api/versions/{id}/design/graph`、`GET /api/versions/{id}/design/cases/{caseId}`、`GET /api/versions/{id}/design/overview`；P3 不要依赖设计对话关闭。
- **事件**：`design.case_confirmed`、`design.case_version_created/stale/superseded`、`design.graph_delta`；执行侧以 `case_confirmed` 作为新确认用例的增量信号，但仍以 DB current+confirmed 为准。
- **契约**：`contracts/openapi.yaml` P2 部分、`events.md` §11、`db-schema.md` §8、`migrations-order.md` §5b、`task-types.md` §3、`agent-profiles-tools.md` §4/4a。
- **任务/Agent**：P3 可复用 work queue/leases/fencing/context/SSE；不要复用 design 的 `case.graph_propose` 写执行结果，执行产出应走 P3 自有表/工具。

前置检查：

1. `scripts/test.ps1` 全绿；P1/P2 验收证据可复现。
2. 不改 P2 frozen 的用例/导出契约；需要破坏性变更先停止并更新 contracts/决策/handoff。
3. P3 迁移只追加；不得修改 `design_0001`/`storage_0002`。
4. 执行只读 current+confirmed；stale/superseded/draft 不得执行。

## 7. 风险

1. **真实模型是外部依赖**：超时/限流/网络失败会进入 `ai_unavailable`；验收必须容忍等待，但真实失败不得用 fake 替代（P2-D-30）。
2. **用例膨胀**：200 用例/100 场景上限是 P2 的防滥用边界；P3 执行需要分页/增量，不能假设整图常驻内存。
3. **确认与执行竞态**：用户确认后执行可能立即读取；执行必须用 DB 事务快照/版本 ID，不能依赖 SSE 顺序。
4. **导出与报告**：P2 `export_artifact` 只有 case；P3/P4 报告/链路导出需扩展 `owner_kind` 并复用内容账本，不要另起文件目录。
5. **契约冻结**：P2 后任何字段/状态机变更都必须走变更控制，不得静默漂移。
