# Phase 03 Handoff — 执行（intent/波次/多 SUT/证据/接管）

> 状态：**completed**
> 日期：2026-11-01
> 阶段：P3（`phase-03-execution.md`）
> 实施分支：`feat/v0.2-p3`（从 `feat/v0.2-p2` @`b4123c8` 拉出）；**已合入统一集成分支 `feat/v0.2`（2026-11-01，P3 完成点）**
> 实施 spec：[2026-11-01-eztest-v02-p3-execution-design.md](../../superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md)
> 实施 plan：[2026-11-01-eztest-v02-p3-execution.md](../../superpowers/plans/2026-11-01-eztest-v02-p3-execution.md)

## 0. 启动提示词映射与前置

- 统一阶段分支：`feat/v0.2`（P3 完成点）；后续 P4 从 `feat/v0.2` 拉出并合回该分支。`feat/v0.2-p3` 保留为阶段审计分支。
- 本阶段按 `implementation-phase-plan.md` 阶段表：**P3 → `phase-03-execution.md`；PREV_HANDOFF=`phase-02-handoff.md`；HANDOFF=`phase-03-handoff.md`**。
- 读取了 `Agent.md`（并按用户要求把其 P1–P7 编号标注为 **0.1**，不改变其设计权威）、version-V0.2 设计文档、decision-log/open-decisions、全部 phase 文件、全部 P0/P1/P2 contracts、`ui/mock.html`；未修改 `docs/version-V0.2` 设计文档（除 contracts/ 与本 handoff）。
- 用户裁决：失败/覆盖只落 P3 自有信号（不建 `analysis_run`/`issue`/`report`）；demo 扩展现有 fixture；P3 验收 **仅真实 LLM**；Agent 需支持时间窗口等待（`execution.wait`，含 120s 验收）。
- 前置检查：P2 `scripts/test.ps1` 全绿；P2 contracts `x-frozen: P2`；17 迁移；P1/P2 验收可复现。

## 1. 完成范围

- [x] 新增 execution 限界上下文四层：`domain/application/infrastructure/interfaces/httpapi`；10 张表 + 3 个只追加迁移（17→20）。
- [x] `execution_intent`：current+confirmed case → 自动 intent；partial unique `(case_version_id,environment_fingerprint,code_baseline,purpose) WHERE active`；`retest/manual` 用户意图；取消/superseded；版本 `completed→running` reopen。
- [x] `run_wave`：同版本单活跃波（planned/running/paused）；自动组波；`trigger=auto/manual/retest`；counts 四态/过程态/evidence + `by_version_sut`；pause/resume/cancel。
- [x] `case_run` + `case_run_sut_context`：多 SUT 上下文；步骤级路由；账号角色解析；dispatch epoch/token/lease fencing；offline/过期重派；四态聚合；stale/superseded 取消。
- [x] `step_result`/`assertion_result`：upsert 幂等、expected 由 P2 真值覆写、assertion_seq 派生；写回 epoch/token CAS，迟到写 409 `lease_lost`。
- [x] `evidence`/`evidence_ref`：内容账本（content_ref）+ API 重算 sha256/size；8 kind；≤5MiB/条、≤100/run；自动截图/console/network/http/db/download；不可变；鉴权下载校验版本归属与 sha/size；不暴露路径/凭据。
- [x] `executor_instance`：pull healthz 注册/心跳/容量/活跃 context；drain/activate（admin）；offline 超时；单 endpoint（P3）。
- [x] `human_request`：login_takeover create/open → resolve（2FA code）/cancel/expire；case/wave paused、work item 取消在飞 run；恢复 epoch+1、从首个未完成步骤续跑；同 case 最多 3 次；超时 failed(session)。
- [x] Case Executor（API 内 Agent）逐动作驱动 Playwright executor；Failure/Coverage Analyst；Execution Planner 确定性 runtime。
- [x] P3 Agents：`case_executor`（60/780/900）、`failure_analyst`（6/180/300）、`coverage_analyst`（6/180/300）；26 个 P3 工具（browser/http/db/execution/human/account/schema/analysis）；scope 不含 version_id。
- [x] `execution.wait`：API 侧 1–300s/次，可被暂停/取消/lease 丢失中断；页面定时器继续；累计计入 case 超时；120s 窗口验收通过。
- [x] 失败分类：`application/environment/session/model/automation/timeout/executor_lost/blocked/unknown`；仅 `failed(application)` 产问题候选事件 + execution 团队帖；无 issue/report/analysis_run 表。
- [x] 覆盖分析：requirement 执行覆盖评分；缺口 ensure P2 `design.gap`（dedupe `gap:{requirement_id}:{case_version_id|requirement_version_id}`）+ `execution.coverage_gap` 事件；`feature.auto_gap_design` 控制。
- [x] REST API 22 operations / 21 path items：overview/intents/waves/case-runs/evidence/human-requests/admin executors；ListEnvelope/before/limit；错误复用 P0 + `evidence_too_large`/`executor_unavailable`。
- [x] 事件 20 个：execution.intent_*、wave.*、case_run.*、step/assertion、evidence.created、human_request.*、failure_candidate、coverage_gap；channel `version:{id}`。
- [x] 前端 `features/execution`：三栏（波次/实时/结果+证据）、暂停/恢复/取消、立即/优雅暂停、登录接管卡、证据查看器（objectURL/下载/sha）、SUT 过滤、SSE、`?wave=`/`?case_run=` 深链、空/错/加载态；61 前端测试文件 / 479 tests。
- [x] demo fixture：orders(13335)/payments(13337) 双变体；2FA `246810`；timeout-window 10s/120s；slow；refund；DB 夹具 `p3_orders`/`p3_refunds`。
- [x] 验收：真实 DeepSeek API 16/16 场景 PASS（默认 316.2s；`-LongWait` 384.3s）；浏览器 E2E PASS（2.7m）；P1 19 steps、P2 21 steps、P2 E2E PASS；全量门禁 97 Go packages ok / 0 FAIL、前端 479 tests。
- [x] contracts 冻结 `P3`；`executor-protocol.md` 新建并冻结；`phase-03-handoff.md`（本文件）。

## 2. 契约变更

| 类型（API/事件/表/任务/工具/设置） | 变更 | 是否 frozen | 影响阶段 |
|---|---|---|---|
| API | `openapi.yaml`：`info.version: 0.2.0-p3`；22 ops/21 path items；22 schemas；tags Execution/Evidence/HumanRequests；错误码 +`evidence_too_large`/`executor_unavailable`；EvidenceView 增加可选 `step_seq`/`assertion_seq` | `x-frozen: P3` | P4–P8 |
| 事件 | `events.md` §12：20 个 P3 事件；`case_run.step_finished`/`case_run.assertion_finished`（HLD 示例 `step.finished` 映射）；payload 不含证据字节/路径/凭据 | `frozen: P3` | P4–P8 |
| 表 | `db-schema.md` §9：`execution_intent`/`run_wave`/`case_run`/`case_run_sut_context`/`step_result`/`assertion_result`/`evidence`/`evidence_ref`/`executor_instance`/`human_request` | `frozen: P3` | P4–P8 |
| 迁移 | `execution_0001_intent_wave.sql`、`execution_0002_result_evidence.sql`、`execution_0003_executor_human.sql`；`order.txt` 第 18–20；前 17 个未改 | `frozen: P3` | P4+ |
| 任务 | `execute.case`、`analyze.failure`、`analyze.coverage`（从 §4 移出）；`analyze.wave` 视为简写不注册；`report.*`/`issue.triage`/`version.close` 仍 P4 | `frozen: P3` | P4+ |
| 工具 | `agent-profiles-tools.md` §6：3 profiles；browser.*、http.request、db.query、database.schema_read、account.credentials、execution.step_record/assertion_record/evidence/wait/case_finish、human.request、analysis.*；scope 无 version_id | `frozen: P3` | P4+ |
| 设置 | `settings.md` 44→49：`execution.executor_token`（secret）、executor_heartbeat_seconds=10、executor_offline_seconds=30、executor_context_ttl_seconds=300、human_request_timeout_seconds=1800 | `frozen: P3` | P4+ |
| 执行器协议 | 新建 `executor-protocol.md`：protocol v1.1、Bearer、healthz、contexts/actions、metadata/GET /v1/contexts、pull 注册/心跳/容量/排空/offline、artifact→content ledger、多 SUT key、错误码/限额；push `/api/internal/executors/*` 留 P7 | `frozen: P3` | P4/P7 |
| 其它 | `contracts/README.md` P3 冻结状态；`p2-contract-check.cjs` 前向兼容；P2 acceptance SSE 分页；CORS `Idempotency-Key` | — | — |

- **intent 唯一键**：`UNIQUE(case_version_id, environment_fingerprint, code_baseline, purpose) WHERE status IN ('open','claimed','running')`。
- **波次并发默认**：全局 3（running case_run 数）、每 `version_sut` 2、跨系统 1（全局跨系统 case_run 数）；executor context 容量另行预留。
- **破坏性变更**：无。P0/P1/P2 表/字段/迁移未改；P0 CORS 仅增加允许头；P2 acceptance 仅测试分页调整；`Agent.md` 仅 0.1 编号标注。

## 3. 测试与验收证据

| 证据 | 命令 | 结果/路径 |
|---|---|---|
| 全量门禁 | `powershell.exe -File scripts/test.ps1` | EXIT=0；97 Go packages ok / 0 FAIL；frontend 61 files / 479 tests；`.tmp/p3/final-gate.log` |
| P1 回归 | `powershell.exe -File scripts/p1-acceptance.ps1 -FakeLLMPort 13334` | PASS 19 steps；`.tmp/p3/p1-regression.log` |
| P2 回归 | `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334` | PASS 21 steps；`.tmp/p3/p2-regression.log` |
| P2 浏览器回归 | `powershell.exe -File scripts/p2-e2e.ps1` | 1 passed；`.tmp/p3/p2-e2e-regression.log` |
| P3 真实 LLM API 验收 | `powershell.exe -File scripts/p3-acceptance.ps1` | PASS 16/16 scenarios，316.2s；`.tmp/p3-acceptance/acceptance.log` / `scenarios.txt` |
| P3 120s 时间窗口 | `powershell.exe -File scripts/p3-acceptance.ps1 -LongWait` | PASS 16/16，384.3s；S14 timeout-window ms=120000 + wait 122，135.0s |
| P3 浏览器 E2E | `powershell.exe -File scripts/p3-e2e.ps1` | 1 passed (2.7m)；`.tmp/p3-acceptance/e2e-playwright.log`；截图 `.tmp/p3-acceptance/screenshots/` |
| 契约冻结 | `node scripts/p3-contract-check.cjs` | OK：81 paths / 110 ops / 122 schemas；20 events；10 tables；20 migrations；3 tasks/profiles；49 settings；executor-protocol |
| P2 契约兼容 | `node scripts/p2-contract-check.cjs` | OK：P2 increments present |
| 迁移幂等 | `go test -tags=integration ./internal/db/...` + acceptance 二次启动 | 20 migrations applied twice no-op；10 P3 tables |
| 真实 LLM 场景矩阵 | 见 `.tmp/p3-acceptance/scenarios.txt` | S1–S16 全 PASS（含单 SUT、跨系统、失败候选、覆盖缺口、2FA 接管、暂停恢复、offline 重派、stale、retest、SSE、120s 窗口、证据） |

- 单测/集成：execution domain 93.2% 覆盖；9 个 postgres 集成测试；server P3 routes/read API 集成；agent P3 profiles/tools/sink；frontend 58 execution tests。
- 验收使用真实 DeepSeek `deepseek-flash`（`.env` lowercase `llm_*`），未用 fake LLM 替代；真实失败未被伪造。
- 验收 DB：隔离 `eztest_v02_e2e`；API 14333；Web 16868；executor 14336（随机 token 经 admin settings 设置）；fixtures 13335/13337；fixture DB `p3_orders`/`p3_refunds`。

## 4. 已知缺口/偏差

| 缺口/偏差 | 影响 | 是否阻塞下一阶段 | 决策记录引用 |
|---|---|---|---|
| 执行器注册为 API pull healthz；HLD `/api/internal/executors/*` push 端点未实现 | 单执行器 P3 可运行；多执行器/P7 需补 push/claim/结果回写 | 否（P7 前） | P3-D-01 |
| 执行器 token 存 DB secret；执行器进程用自身 env；默认 `eztest-dev-executor-token` | 非 loopback 部署必须改 token | 否 | P3-D-02/P3-D-19 |
| 证据字节入 content ledger；`paths.evidence_dir` 不使用 | P7 D65 GC 需消费 content_ref；目录设置空置 | 否 | P3-D-20 |
| 未建 `analysis_run`/`issue`/`report`；失败候选/覆盖缺口以事件+团队帖+`design.gap` 交付 | P4 必须建交付表并从事件/`case_run` 重建 | 否（P4 设计输入） | P3-D-13/P3-D-18 |
| 登录接管为暂停-恢复 + 一次性 2FA code；无远程浏览器/VNC；`storage_state` 导入未做 | 复杂验证码/SSO 场景只能 blocked；P7/P8 可增量 | 否 | P3-D-17 |
| `human_request` 使 case/wave paused 并取消在飞 agent run；work item 状态不驻留 `waiting_human`（工具调用后 run 结束，resolve 后新建 execute.case） | P4 收尾谓词只依赖 open human_request/paused wave，不依赖 waiting_human task | 否 | P3-D-25 |
| `CaseReader` 仅 current+confirmed；历史/已 superseded 的 `case_title`/`case_id` 在 intent/case-run view 中为空 | P4 链路/历史视图需补历史 case reader | 否（P4 可增量） | P3-D-04 |
| overview `case_run_counts` 由 wave counts 聚合（最多 1000 波），非全历史精确聚合 | 概览计数可能受波次保留上限影响 | 否 | P3-D-29 |
| `GET /waves?status=` 由 handler 内存过滤一页；前端未使用 | 大数据量时状态过滤不完整 | 否 | — |
| P2 stale hook 非同一事务 | 执行以 DB 真值 + 30s reconciler 兜底；短时事件顺序可能滞后 | 否 | P2 遗留 |
| 真实 LLM 外部依赖（超时/限流/输出波动） | 验收可能需重试；失败如实 blocked，不用 fake 替代 | 否（P8 质量） | P3-D-33 |
| 验收用例通过 SQL 预置 draft + P2 确认 API；未跑设计 LLM | P2 已验收设计流程；P3 聚焦执行 | 否 | P3-D-33 |
| `execution.wait` 单次 ≤300s，累计受 `execution.case_timeout_seconds`（900s 默认） | 更长等待需调设置/拆用例 | 否 | P3-D-41 |
| `agent.max_turns=12` 对 P3 profiles 不生效（用 profile 60）；P1/P2 仍受 12 限制 | 行为按 spec §7.2，已加回归测试 | 否 | spec §7.2 / Task 8 |
| 开发库 `eztest_v02` 仅应用 P0 迁移时需先启动 API 补齐 20 | 手工演示前先启动 API | 否 | — |

## 5. 下一阶段输入（P4 交付/链路）

**必须读取的契约/表/接口**
- contracts：`openapi.yaml`（P3 paths/schemas）、`events.md` §12、`db-schema.md` §9、`migrations-order.md` §5c、`task-types.md` §5、`agent-profiles-tools.md` §6、`settings.md` §4c、`executor-protocol.md`。
- 表：`execution_intent`、`run_wave`、`case_run`、`case_run_sut_context`、`step_result`、`assertion_result`、`evidence`、`evidence_ref`、`executor_instance`、`human_request`；P2 `case_*`/`design_scope`。
- API：`GET /api/versions/{id}/execution/overview`、`/waves`、`/waves/{id}`、`/waves/{id}/case-runs`、`/case-runs/{caseRunId}`、`/evidence`、`/evidence/{id}/content`、`/human-requests`、`POST /human-requests/{id}/resolve`。
- 事件：`wave.created/started/paused/resumed/finished`、`case_run.started/paused/resumed/finished`、`case_run.step_finished/assertion_finished`、`evidence.created`、`execution.failure_candidate`、`execution.coverage_gap`、`human_request.created/resolved/expired`。
- 任务：`analyze.failure`/`analyze.coverage` 已实现；P4 新增 `issue.triage`/`report.wave`/`report.version`/`version.close`，不要改 P3 类型语义。

**P4 可消费的交接数据**
- 问题候选：`case_run.failure_class=application` + `execution.failure_candidate` payload `{case_run_id,failure_class,category,severity,dedupe_hint}` + `team_room_post(team=execution,kind=finding,refs)`；P4 建 `issue`/dedupe/confirm/dismiss/merge。
- 覆盖缺口：`design.gap` work item（已由 P2 设计 reconciler 消费）+ `execution.coverage_gap` 事件；P4 不要重复建。
- 报告：P3 不建 report；P4 从 `run_wave.counts`（含 `by_version_sut`）/case_run/step/assertion/evidence 生成 wave/version 报告；`run_wave.report_id` 留空可回填。
- 链路：case_run/evidence/issue/report 事件 + 读模型；P3 表提供 as-of 数据。
- 版本收尾：P3 已提供 `version.Reopen`（completed→running on new intent）；P4 实现 close 谓词/`version.close`/report.version。
- human_request：P4 待办入口读 open human_request；resolve 已在 P3。

**前置检查**
1. `scripts/test.ps1` 全绿；P3 contracts frozen；20 迁移。
2. 不改 P3 表/事件/任务/工具语义；破坏性变更先停止并更新 contracts/决策/handoff。
3. P4 只能增量迁移；不改 execution_0001–0003。
4. P4 issue/report/analysis_run 表不属于 P3，按 P4 设计新建。

**建议先做**
- issue/report/analysis_run 表与 API；wave 终态 report/issue 任务；close 谓词；chain 读模型；human_request 待办入口。

## 6. 风险

1. **跨系统执行**：多 context/配额预留/登录/证据归属；P3 已用 2 SUT 最小闭环 + 配额矩阵测试，P4 链路/报告需按 `version_sut_id` 分组。
2. **执行器离线/租约竞态**：offline 检测 + epoch/token 重派已验收；P4/P7 多实例需 push 注册/共享存储/跨实例 fan-out。
3. **证据体积**：5MiB/条、100/run、content ledger；P7 GC 需保护被引用证据；报告只放缩略图/计数。
4. **真实 LLM 不稳定**：Case Executor/Failure/Coverage 验收可能波动；P8 做质量评测；不得以 fake 替代真实失败。
5. **登录接管体验**：无远程可视化；复杂 2FA/验证码可能 blocked；后续可加 storage_state/可视接管。
6. **历史用例视图**：current+confirmed-only reader 导致 superseded 标题为空；P4 链路需补历史 reader。
7. **契约冻结**：P3 后任何字段/状态机/事件变更走变更控制；push executor 端点、`analysis_run` 属 P7/P4，不得提前塞入 P3。
8. **默认执行器 token**：dev 已知；非 loopback 部署必须轮换并审计。
