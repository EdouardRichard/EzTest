# Phase 04 Handoff — 交付 + 链路（Issue/Report/Closure/Close/Chain）

> 状态：**completed**
> 日期：2026-11-01
> 阶段：P4（`phase-04-delivery-chain.md`）
> 实施分支：`feat/v0.2-p4`（从 `feat/v0.2` @`5b1e326` 拉出；本文件完成后合入 `feat/v0.2`）
> 实施 spec：[2026-11-01-eztest-v02-p4-delivery-chain-design.md](../../superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md)
> 实施 plan：[2026-11-01-eztest-v02-p4-delivery-chain.md](../../superpowers/plans/2026-11-01-eztest-v02-p4-delivery-chain.md)

## 0. 启动提示词映射与前置

- 按 `implementation-phase-plan.md`：P4 → `phase-04-delivery-chain.md`；PREV_HANDOFF=`phase-03-handoff.md`；HANDOFF=本文件。
- 读取了 `Agent.md`、`docs/version-V0.2/README.md`、HLD/architecture、decision-log、capability-matrix、open-decisions、implementation-phase-plan、phase-04/03、P0–P4 contracts、`ui/mock.html`；未修改设计文档（除 contracts/、本 handoff 与 spec/plan 实施记录）。
- 用户裁决：P4 含最小需求闭环；报告 HTML 入内容账本（A）；基础 reopen（A）；链路两层历史回看（A）；契约先行 + 并行实施（A）。
- 前置：P3 handoff 完成；P3 contracts `frozen: P3`；20 迁移；P1/P2/P3 验收可复现。

## 1. 完成范围

- [x] 新增 `delivery` 限界上下文四层：issue 生命周期/去重/merge、report 快照/指纹/HTML 渲染/内容账本/修订/as-of/stale/regenerate、analysis_run、issue/report HTTP API、真实 ReportDataReader/TriggerReader/ExecutionReader。
- [x] 新增 `chain` 只读上下文：领域读模型 + 5 个 application provider + event cursor/time provider + 历史 CaseReader + HTTP API；**无 chain 表**；预算/过滤/focus/截断/两层历史回看。
- [x] 扩展 `version`：requirement closure（8 谓词、事务内复核、append-only close round、closed→draft、reopen 计数）、version close（8 谓词、自身 version.close 排除、`completed` 条件更新、closed 字段、事件/审计）、manual/requirement/design/intent reopen 投影。
- [x] 扩展 `agent`：3 个 P4 profiles、5 个任务类型、8 个任务域工具（含复用 `requirement.read`）、payload-only task contexts、P4 reconciler/`P4Ensurer`、Report/Requirement finalizer、tool schema 修复。
- [x] 前端：Issues / Reports / Chain 三个 Tab 替换占位；VersionHeader（closed 投影 + reopen）、TodoDrawer（human_request）、SSE 全事件刷新、深链。
- [x] 数据：4 个迁移（#21–#24）、8 张表、10 事件、14 API operations、4 `chain.*` 设置；D34 命名规范用于 P4 新表；P0–P3 历史命名问题记 `P4-D-16`，P4 不返工。
- [x] 并发/一致性：version close × execution intent 与 requirement closure × intent 用同键 PostgreSQL 事务 advisory lock 串行化；issue 状态机在行锁内复核；report 只允许单调 finalize；旧 revision 不可变。
- [x] 真实 LLM 验收：P4 API/DB 13/13 PASS（`deepseek-flash`），浏览器 E2E 两阶段 PASS；P1/P2/P3 回归见 §3。
- [x] contracts 冻结 `P4`；本 handoff。

## 2. 契约变更

| 类型 | 变更 | 是否 frozen | 影响阶段 |
|---|---|---|---|
| API | `openapi.yaml` `info.version: 0.2.0-p4`，`x-frozen: P4`；14 operations（6 issue、5 report、1 chain、version/requirement reopen）；P0–P3 仅增加可选字段 | `P4` | P5+ 只能增量 |
| 事件 | `events.md` §13：10 个 P4 事件（issue.created/updated/reopened/merged、report.*、requirement.closed/reopened、version.closed/reopened） | `P4` | P5+ |
| 表 | `db-schema.md` §5/§10：`delivery_issue`、`delivery_issue_ref`、`delivery_issue_status_history`、`delivery_report`、`delivery_report_revision`、`delivery_report_ref`、`delivery_analysis_run`、`requirement_closure`；**chain 无表** | `P4` | P5+ |
| 迁移 | `delivery_0001_issue_lifecycle` #21、`delivery_0002_report_revision` #22、`delivery_0003_analysis_run` #23、`version_0005_requirement_closure` #24；`order.txt` 24 | `P4` | P5+ |
| 任务 | `task-types.md` §6：`issue.triage`、`report.wave`、`report.version`、`requirement.close`、`version.close` | `P4` | P5+ |
| 工具 | `agent-profiles-tools.md` §7：`issue_triager`/`report_writer`/`version_steward`；8 个任务域工具（`requirement.read` 复用） | `P4` | P5+ |
| 设置 | `settings.md` §4d：49→53，`chain.max_nodes`/`max_edges`/`max_requirements`/`replay_event_limit` | `P4` | P5+ |
| 其它 | `contracts/README.md` P4 冻结状态；`p2/p3` 契约检查改为前向兼容；P4 acceptance/E2E 脚本 | — | — |

- **破坏性变更**：无。P0–P3 表/字段/迁移未改；P0–P3 API 仅新增可选响应字段。
- **chain 无表**：链路是只读快照，从 P1–P4 真值表 + 事件游标构建；不做任意 as-of。
- **报告存储**：HTML 入内容账本（`content_ref.owner_kind=report_revision`、`role=report_html`）；不写 `paths.report_dir`；P4 无 PDF。
- **需求闭环**：存在 issue ref 的开放 issue 不阻塞；`failed(application)` case run 必须有 issue ref；`requirement.close` 自身任务从 open work 计数排除。

## 3. 测试与验收证据

| 证据 | 命令 | 结果/路径 |
|---|---|---|
| 全量门禁 | `powershell.exe -File scripts/test.ps1` | EXIT=0；Go unit 全包 ok；Go integration 全包 ok；frontend 66 files / 548 tests；`.tmp/p4/final-gate.log` |
| P4 契约 | `node scripts/p4-contract-check.cjs` | OK：14 P4 ops；95 paths/124 ops/148 schemas；8 表；4 迁移 #21–#24；10 事件；5 任务；3 profiles；8 工具；4 设置 |
| P3 契约兼容 | `node scripts/p3-contract-check.cjs` | OK：95 paths/124 ops/148 schemas；20 事件；10 表；24 迁移；53 设置；executor protocol |
| P2 契约兼容 | `node scripts/p2-contract-check.cjs` | OK：P2 increments present |
| P4 真实 LLM API/DB 验收 | `powershell.exe -File scripts/p4-acceptance.ps1 -PollTimeoutSeconds 900` | EXIT=0；13/13 PASS（226.1s）；S0/S0b/S1/S2/S3/S4/S4b/S5/S6/S6b/S7/S8/S9；`.tmp/p4-acceptance/scenarios.txt` / `acceptance.log` |
| P4 浏览器 E2E | `powershell.exe -File scripts/p4-e2e.ps1` | EXIT=0；phase A 1 passed (1.4m)，phase B 1 passed (2.8s)；17 screenshots + 2 HTML；`.tmp/p4-acceptance/e2e.log`、`screenshots/` |
| P1 回归 | `powershell.exe -File scripts/p1-acceptance.ps1 -FakeLLMPort 13334` | PASS 19 steps；`.tmp/p4/p1-regression.log` |
| P2 回归 | `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334` | PASS 21 steps；`.tmp/p4/p2-regression.log` |
| P2 浏览器回归 | `powershell.exe -File scripts/p2-e2e.ps1` | 1 passed；`.tmp/p4/p2-e2e-regression.log` |
| P3 真实 LLM 回归 | `powershell.exe -File scripts/p3-acceptance.ps1` | PASS 16/16 scenarios，329.5s；`.tmp/p4/p3-regression.log`；`.tmp/p3-acceptance/scenarios.txt` |
| P4 契约/迁移兼容 | `go test -tags=integration ./internal/db/...` + 验收二次启动 | 24 migrations applied twice no-op；P3 回归对 P4 8 表做前向兼容断言 |
| 迁移幂等 | `go test -tags=integration ./internal/db/...` + acceptance 二次启动 | 24 migrations applied twice no-op；8 P4 表 |
| 真实 LLM 场景矩阵 | `.tmp/p4-acceptance/scenarios.txt` | S0–S9 全 PASS：issue 生命周期/跨波 dedupe、report HTML 账本/修订/stale/regenerate、closure 8 检查、version close 自身排除/closed 字段/事件、requirement/manual/requirement_created reopen、chain 过滤/深链/历史/截断、2FA human_request、pending-design 阻塞后闭环 |

- 验收使用真实 DeepSeek `deepseek-flash`，未用确定性 fake LLM 替代；真实失败作为系统缺陷修复（本轮真实验收发现并修复了 API 版本常量、version-close 执行读取器错配、requirement.close 409 后无法重试、issue 状态机锁内复核、report 回退发布、chain 需求根截断/事件区间场景丢失、前端 SSE 刷新、无参工具 schema 等）。
- 验收 DB：隔离 `eztest_v02_p4`；API 14344；Web 16869；executor 14346；fixtures 13335/13337；target DB `eztest_v02_test`。
- E2E 使用 `chain.max_nodes=5` 启动 API，真实触发 UI 截断 Alert；API 验收的截断用 lower-only `limit` 覆盖验证。

## 4. 已知缺口/偏差

| 缺口/偏差 | 影响 | 是否阻塞 P5 | 决策记录/备注 |
|---|---|---|---|
| `chain.max_nodes` 等预算在 chain service 构造时读取一次，PATCH 设置不热更新 | 运行中调预算需重启；验收用 `limit` 下压/E2E 启动参数验证截断 | 否 | spec §4.5 热更新留 P5 质量项 |
| `feature.auto_gap_design=false` 用于 P4 真实验收 | 隔离 P2 design-gap agent 对失败 case 的新 draft 版本，避免其阻塞 P4 closure；P3 验收覆盖 design.gap | 否 | 真实跨阶段交互已记录 |
| `coverage_recorded` 用“当前+确认 case 有可读最新 case_run”代理 P3 覆盖投影 | 无 P3 coverage 表；当前语义满足 closure 谓词但非 P3 评分 | 否 | Task 23 report |
| 历史 case_version 链节点缺 requirement/scenario 完整上下文；链过滤会丢弃其 SUT/page 上下文 | 历史节点只读展示/`executed_by`；不影响当前图 | 否 | P4-D-04/Task 18 |
| 报告列表服务端最多取最近 100 条，再由 handler 分页 | 超过 100 的报告不可翻页 | 否 | Task 7/12 report |
| `chain.replay_event_limit` 已 seed，但 event cursor 未消费该上限 | 事件区间解析在长 channel 上可能退化 | 否 | Task 24 finding |
| `chain` Node 结果/mention meta 为只读展示；未覆盖 case_version 聚合结果 | UI 以 run 结果为准 | 否 | Task 18 report |
| P3 `execution_coverage_read` 无参工具 schema 曾返回 `properties:null`，真实 LLM 400；已修（`305e161`） | 修复后 coverage 分析可调用；P3 验收需回归 | 否 | P3 tool schema |
| `paths.report_dir`/`report.render_pdf` P4 不消费；PDF 留 P7 | 无 PDF | 否 | P4-D-xx / spec |
| 全局中心页、任意 as-of、code/visual 中心、D65 GC 等 | P5–P7 范围 | 否 | phase plan |
| 遗留 P0–P3 表约束/布尔列命名不符合 D34 | 仅历史表；P4 新表合规 | 否 | P4-D-16 |
| `chain.max_*` 热更新、`replay_event_limit` 未消费 | 见上 | 否 | Task 24/18 |

## 5. 下一阶段输入（P5）

**必须读取**
- P4 contracts：`openapi.yaml`（P4 paths/schemas）、`events.md` §13、`db-schema.md` §5/§10、`migrations-order.md` §6、`task-types.md` §6、`agent-profiles-tools.md` §7、`settings.md` §4d。
- P4 表：`delivery_issue`/`delivery_issue_ref`/`delivery_issue_status_history`、`delivery_report`/`delivery_report_revision`/`delivery_report_ref`、`delivery_analysis_run`、`requirement_closure`；不建 chain 表。
- API：issue CRUD/confirm/dismiss/reopen/merge；report list/detail/revision/html/regenerate；`GET /api/versions/{id}/exploration-chain`；version/requirement reopen。
- 事件：issue.*、report.*、requirement.closed/reopened、version.closed/reopened；channel `version:{id}` + `system`。
- 任务/工具：`issue.triage`、`report.wave`、`report.version`、`requirement.close`、`version.close`；8 工具；scope 注入，payload-only task context。
- 闭包/close 语义：8 谓词、事务 advisory lock、`version.close` 自身排除、`closed_at/close_reason/close_run_id`、reopen 触发。

**P5 可消费的交接数据**
- Issue：open/confirmed/dismissed/merged + refs/history/triage；dedupe key 稳定；merge 终态。
- Report：版本/波次报告 + 修订 + HTML 内容账本指针/sha/size；as-of/stale/render_status；旧 revision 可下载。
- Closure：requirement_id/version_id/close_round/checks/evidence/closed_by_run_id；closed→draft。
- Version close：completed 投影 + 事件 + 报告指纹；reopen 后 running。
- Chain：只读快照（nodes/edges/counts/replay/cursor）与 5 providers；无表、无任意 as-of。
- Human todo：open human_request + paused wave + pending design。

**前置检查**
1. `scripts/test.ps1` 全绿；P4 contracts frozen；24 迁移；P4 acceptance/E2E 已通过。
2. 不改 P0–P4 表/事件/任务/工具语义；破坏性变更先停止并更新 contracts/决策/handoff。
3. P5 只能增量迁移（从 #25 起）；`chain.*` 热更新/`replay_event_limit` 作为质量项处理。
4. 真实 LLM 验收保留；禁止 fake 替代 acceptance。

## 6. 合并与审计

- 实施分支：`feat/v0.2-p4`；完成点合入 `feat/v0.2`（保留阶段审计分支）。
- 关键修复提交：`bf395bf` API 版本；`09a00fc` version close 执行读取器；`1ed7c4e` close×intent 串行化；`b719854` requirement.close finalizer；`6f4ed85` chain 根分页/场景 replay；`a7b9a7b` issue CAS/report 单调；`3649fc4` 前端 SSE；`aca66c2` 验收脚本；`305e161` 无参工具 schema。
- 本阶段报告/证据：`.superpowers/sdd/p4-delivery-chain/task-*-report.md`、`.tmp/p4-acceptance/`、`.tmp/p4/`。
