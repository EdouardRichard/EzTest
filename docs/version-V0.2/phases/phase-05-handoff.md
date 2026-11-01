# Phase 05 Handoff — 代码智能 + 视觉断言（P5）

> 状态：**completed（API/DB 真实 LLM + 真实 Chrome 验收 13/13 PASS；浏览器 E2E 2/2 PASS）**
> 日期：2026-11-01
> 阶段：P5（`phase-05-codeintel-visual.md`）
> 实施分支：`feat/v0.2-p5`（从 `feat/v0.2` @`ff1e176` 拉出；**未合入** `feat/v0.2`）
> 实施 spec：[2026-11-01-eztest-v02-p5-codeintel-visual-design.md](../../superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md)
> 实施 plan：[2026-11-01-eztest-v02-p5-codeintel-visual.md](../../superpowers/plans/2026-11-01-eztest-v02-p5-codeintel-visual.md)

## 0. 启动提示词映射、前置与流程

- 按 `implementation-phase-plan.md`：P5 → `phase-05-codeintel-visual.md`；PREV_HANDOFF=`phase-04-handoff.md`；HANDOFF=本文件。
- 读取了 `Agent.md`、`docs/version-V0.2/README.md`、HLD/architecture、decision-log、capability-matrix、open-decisions、implementation-phase-plan、phase-04/05、P0–P5 contracts、`ui/mock.html`；**未修改 `docs/version-V0.2` 设计文档**（仅 contracts、本 handoff、spec/plan 实施记录）。
- 用户裁决：
  - 根因记录采用 **A**：只在 P5 spec/handoff 记录“phase-05 详细设计缺失”的调查结论，不改 `docs/version-V0.2` 设计文档。
  - 方案 A：批准 P5 spec；批准 plan 后开始实施。
  - 执行方式：子代理并行实施、Lead 审查/提交/验收；实施阶段不等待逐任务确认。
- 根因（供 P6+ 防再犯）：0.2 的 `docs/version-V0.2/**` 全部在 `9173c52`（2026-10-31 16:59）一次提交落盘；`phase-05-codeintel-visual.md` 仅 2.5 KB 且从未修订；其引用是手工拼装的混合范围（ARCH §6.3=design、§7.3=tools、§8.4–8.5=flows、§17=evolution；D20–D27 实为 phase-02 范围、D39–D44 实为 phase-01 范围）。启动提示只列 0.2 文档，未引导到 0.1 的详细设计/决策原始来源。**P5 因此从 spec 重建**；P6/P7 同类引用漂移风险见 §7。

## 1. 完成范围

- [x] 代码智能上下文（`backend/internal/codeintel`，四层）：只读 gitx（argv/超时/路径逃逸/UTF-8/限额）；T0 索引/树/文件/diff/commit/search；T1 真实 LLM facts（证据服务端校验、fact version、supersede、cache、degraded）；影响候选 direct/contract/relation/case + 一次 AI 推断；锚点 valid/stale/broken + 复核 append-only；页面/API 提议（code facts + 网络证据、admin 确认/驳回）。
- [x] 视觉断言上下文（`backend/internal/visual`，四层）：配置校验/默认值/config_revision、基线 admin 审批（PNG/SHA/尺寸/归属/config revision）、视觉比对记录、四态分类、质量总览；追加 `visual_assertion`/`visual_baseline`/`visual_diff`。
- [x] 执行接线：`execution.visual_assert` 工具、`CaseRunner` 视觉四态、`visual_actual`/`visual_diff` 证据、视觉计数、`visual_assertion_not_run` 兜底；**视觉断言结果优先于 agent 自由文本 step 状态**（P5-D-20）。
- [x] 版本：`version_sut.code_baseline_commit`、`code_baseline` 解析器、执行意图指纹（多 repo 排序 SHA-256）、commit-hash 升级。
- [x] Agent：`code` 团队 + `index.repo`/`code.analyze` 任务、`code` worker、11 个工具、5+1 profile 增量（P5-D-19 增加 `design_host` 读码）、上下文暴露 visual assertion IDs。
- [x] 前端：8 tab IA 的“代码与变更”完整工作区；执行 `?view=visual` 子视图；`/quality` 视觉质量页；锚点共享面板（design/requirement/issue）；提议生成/确认/驳回；SSE `code.*`/`visual.*`。
- [x] 数据：7 个迁移 #25–#31；11 张表；12 事件；38 operations；2 任务 + `code` 团队；11 工具；16 设置；10 证据 kind；6 work item team。
- [x] 真实验收：`scripts/p5-acceptance.ps1` 真实 DeepSeek `deepseek-flash` + 真实 Chrome，**S0–S12 13/13 PASS，EXIT=0**。
- [x] contracts 冻结 `P5`；`scripts/p5-contract-check.cjs` 门禁；本 handoff。

## 2. 契约变更（全部 frozen: P5）

| 类型 | 变更 | 影响阶段 |
|---|---|---|
| API | `openapi.yaml` `0.2.0-p5`、`x-frozen: P5`；+38 operations（34 path item）；新增 `Code`/`Visual`/`Quality` tags；P0–P4 仅加可选 `Binding.code_baseline_commit`、`CaseRunSummaryView` 视觉计数、`EvidenceView.kind` 扩宽、`DesignAssertion.case_assertion_id` | P6+ 只能消费 |
| 事件 | `events.md` §14：12 事件（code.index/analysis/fact/review/anchor/impact/proposal；visual.baseline/diff） | P6+ |
| 表 | `db-schema.md` §11–§13：codeintel 8 + visual 3；`version_sut.code_baseline_commit`；`evidence.kind` 10 值；`work_item.team` 6 值 | P6+ |
| 迁移 | `codeintel_0001_code_index` #25、`codeintel_0002_code_analysis` #26、`codeintel_0003_anchor_review` #27、`visual_0001_visual_assertion` #28、`execution_0004_visual_evidence` #29、`version_0006_version_sut_code_baseline` #30、`agent_0004_code_team` #31；`order.txt` 31 | P6+ |
| 任务 | `index.repo`、`code.analyze`；`code` 团队（agent worker 不 claim） | P6+ |
| 工具 | 11：`code.tree/read/search/facts/routes/apis/diff/impact/anchors`、`execution.visual_assert`、`sut.page_propose` | P6+ |
| Profile | context_scout/case_author/failure_analyst/design_planner/design_host/case_executor 增量白名单 | P6+ |
| 设置 | 53→69：codeintel 9 + visual 7 | P6+ |
| 执行器 | `executor-protocol.md` §11：协议 v1.2，`visual.compare`、5 MiB 视觉请求、环境指纹、actual/diff 证据映射 | P6+ |

- **破坏性变更**：无。P0–P4 表/字段/迁移未改；API 只增可选字段/响应；P1–P4 工具/profile 行为保持。
- 契约门禁：`node scripts/p2/p3/p4/p5-contract-check.cjs` 全 PASS；P5 检查为 38 ops/31 migrations/11 tables/12 events/2 tasks/11 tools/16 settings。

## 3. 测试与验收证据

| 证据 | 命令 | 结果/路径 |
|---|---|---|
| 全量门禁 | `powershell.exe -File scripts/test.ps1` | **EXIT=0**；Go unit+integration 全绿；frontend **87 files / 668 tests PASS**；`.tmp/p5/final-gate-4.log` |
| P5 契约 | `node scripts/p5-contract-check.cjs` | OK（38 ops/31 migrations/11 tables/12 events/2 tasks/11 tools/16 settings） |
| P2/P3/P4 契约兼容 | `node scripts/p2/p3/p4-contract-check.cjs` | 全 PASS（129 paths/162 ops/199 schemas） |
| 前端类型/构建 | `cd frontend; pnpm typecheck` | `tsc --noEmit` EXIT=0 |
| 执行器单测 | `cd executor; pnpm test` | 104 tests / 104 pass / 0 fail |
| **P5 真实 LLM + Chrome API/DB 验收** | `powershell.exe -File scripts/p5-acceptance.ps1` | **EXIT=0；PASS=13 FAIL=0 BLOCKED=0**；`.tmp/p5-acceptance/final7.log`、`scenarios.txt` |
| 迁移幂等 | acceptance S0（两次启动/31 migrations） | 31 applied；第二次 0；隔离 DB `eztest_v02_p5` |
| 验收环境 | — | API 14345、executor 14347、fixture target 13338、DB `eztest_v02_p5`、真实 `.env` DeepSeek、真实 Chrome；fixture A/B 动态 SHA |
| 浏览器 E2E | `powershell.exe -File scripts/p5-e2e.ps1` | **EXIT=0；2 tests PASS（14.9s）**；11 张截图；`.tmp/p5-acceptance/e2e-fix1.log`、`e2e.log`、`screenshots/` |

**P5 API/DB 验收场景矩阵（final7，S4 最后运行以避免设计 agent 异步改写 fixture）**

- S0 门禁/健康：31 migrations、69 settings、p2–p5 契约、真实 LLM 连通、executor active、fixture A/B 确定性。
- S1 T0：index A/B、duplicate 200 reused、tree/file/diff A/M/D + binary、worktree clean。
- S2 T1：真实 LLM analysis degraded=false、facts=21、edges=14、evidenceFailures=0、3 个服务端 evidence SHA 复算、cache reuse、事件无源码。
- S3 影响：6 candidates（direct req/case/page/api + relation req）、union/analysis-detail 持久 ID、PATCH、AI candidate、route_to_api、unmapped。
- S4 设计 Agent：真实 LLM 调用 `code.facts`/`code.routes`/`code.apis` + `case.graph_propose`，设计图 7→10 例。
- S5 锚点：valid/stale/broken、duplicate 409、invalid 422、delete 204→404、事件。
- S6 复核：correct/incorrect append、审计、普通用户可写；incorrect fact 在重算影响中排除（含 cache hit 路径）。
- S7 提议：生成/幂等/来源/确认/驳回/非 admin 403/事件计数。
- S8 视觉首跑：3 个 case 均 visual_actual PNG + inconclusive(missing_visual_baseline)，无 diff 图/issue/false pass；case_run=inconclusive（不再 blocked）。
- S9 基线/重测：admin 批准、非 admin 403、非归属证据 422、light 重测 passed diff_ratio=0 + actual/diff、旧基线 superseded、环境不匹配 inconclusive(environment) 且 case_run=inconclusive(environment)。
- S10 主题差异：shifted failed(application) diff_ratio=0.98033、50 boxes、actual/diff 证据、自动 issue + issue anchor。
- S11 环境/配置：DPR 不匹配 inconclusive(visual_environment_mismatch)；config revision 1→2 supersede 后 inconclusive(visual_config_changed)；重批基线后恢复 passed。
- S12 质量/基线：overview/metrics 一致；网络证据产生 `GET /p5/orders` source=code 提议；`code_baseline_commit=A`；intent `code_baseline` = SHA-256(A)；升级 B 后 intent 指纹变化。

**验收发现并修复的产品缺陷（均在本分支提交）**

1. `6f25933` 首次 visual config 创建 404（`GetByCaseAssertion` 未把 pgx.ErrNoRows 映射为 not found）。
2. `9c139e8` 无 active baseline 返回 404（`Active` 同样映射缺失）。
3. `b51b2cd` union candidate 丢 ID/analysis_id/timestamps；analysis detail candidates 为空；design_host 无 code 工具；视觉 blocked step 优先级。
4. `14bdeb2` anchor `subjectId` 过滤被推断 int4 → snowflake 500（cast bigint）。
5. `a1bb983`/`ff47979` 视觉判定权威性、`visual_config_changed`、网络证据相对 URL。
6. `990608b` 视觉 inconclusive 不被 agent blocked/failed(environment) 覆盖（最终守卫）。
7. `7b51386` 聚合前规范化视觉 step 状态。
8. `e98f6e7` 失败分析重算不得把视觉 inconclusive 改回 blocked。
9. `dcd12f9` fact review 后立即重算影响候选（即使 T1 cache hit）。
10. E2E harness 根因修复（非产品代码）：`Start-Api` 在 PowerShell 函数内把 `$apiProcess` 写成函数局部变量，脚本级变量保持 `null`；bootstrap API 实际未被停止，第二个 API 绑定失败后 health 检查仍命中旧 API，旧 API 缓存了 token 设置前的 executor token，导致 `execute.case` 3/3 `unauthorized`、无 `visual_assert`/diff、E2E 在准备阶段失败。改为 `$script:apiProcess` 并加“启动前端口必须空闲/进程不得立即退出”守卫后，E2E 2/2 PASS。

## 4. 已知缺口/偏差（不阻塞 P6，但需带入）

| 缺口/偏差 | 影响 | 阻塞 P6? | 记录 |
|---|---|---|---|
| `design_host` 增加 `code.facts/routes/apis`（P5-D-19），契约 `agent-profiles-tools.md` §8.1 已改 | 设计对话可直接读码；原 P4 profile 语义扩展 | 否 | spec/handoff |
| `code.analysis_finished` 的 `candidates` 仍为 0（事件在 impact 计算前发布）；`code.impact_computed` 携带真实计数 | 只影响该事件消费者；前端/验收用 impact 事件/接口 | 否 | D4 |
| `code_fact` 无 meta 列，`clue`/`invalidEvidence` 合并入 `body` | fact detail 形状与 0.1 略异；0.2 契约无 meta 列 | 否 | Task 6–8 report |
| T1 “has_anchor 优先”排序延后（当前 path asc）；anchors 在 facts 后创建 | 仅排序优化 | 否 | Task 6–8 |
| `code.search` 无 cursor 分页（返回 `next_cursor=""`/`has_more=false`） | 大结果集受限 | 否 | Task 6–8 |
| 视觉基线审批用 `evidence.created_at >= assertion.updated_at` 作为 config revision 兜底（evidence 无 config_revision 列）；config 更新已 supersede | 旧 actual 理论上可能被批；服务端仍校验归属/SHA/尺寸/PNG | 否 | Task 13–16 |
| 视觉基线环境指纹无 browser name/version（审批时未知） | 浏览器漂移靠 viewport/DPR/尺寸/像素差兜底 | 否 | Task 13–16 |
| 硬执行器失败只写 inconclusive assertion_result，无 visual_diff 行（无 actual bytes） | 质量指标不含 blocked 尝试 | 否 | Task 13–16 |
| `sut.page_propose` 多 SUT 目标：host 匹配页 URL，否则首个绑定环境；无 versionSutId 参数 | 多 SUT 版本提议可能选错环境 | 否 | Task 23 |
| P5 profile 增量只在 fresh DB 的 seed 生效；旧 DB 不覆盖管理员编辑 | 旧 DB 升级后需管理员补白名单 | 否 | Task 12/23 |
| codeintel worker 的 `RecoverExpired` 委托全局 P1 recovery（无 team-scoped recovery） | 租约恢复跨 team，幂等/有 lease guard | 否 | Task 23 |
| `code.analysis_finished` 事件在 impact 前；`code.impact_computed` 才是候选计数来源 | 已有说明 | 否 | D4 |

## 5. 决策记录（P5 实施新增）

- **P5-D-17**：代码索引/分析按 version + repo 绑定；`code` team 独立 worker；不建 `code_change_set`/`code_analysis_cache`/`code_fact_version`。
- **P5-D-18**：T0 只读 Git；T1 真实 LLM facts 必须有服务端可复算证据；degraded 只降级 AI，不伪造候选。
- **P5-D-19**：`design_host` 增加 `code.facts/routes/apis`（S4 要求设计 Agent 读码；契约同步）。
- **P5-D-20**：视觉四态由断言结果/分类驱动，**优先于** Case Executor 自由文本 step blocked/failed；失败分析重算同样遵守。
- **P5-D-21**：config revision 更新 supersede 旧基线；重跑返回 `visual_config_changed`（通过最近基线 revision 判定），而非误报 missing baseline。
- **P5-D-22**：fact review 立即重算 impact（即使 T1 cache hit），incorrect 事实从自动影响中排除。
- **P5-D-23**：网络证据 URL 可为浏览器相对路径，按 version SUT base URL 解析后再 host 校验；接受页面级捕获（如 `GET /p5/orders`）作为 source=code 提议。
- **P5-D-24**：analysis detail #15 由 ImpactService.Detail 填充 candidates，避免与 T1 handler 重复路由。

## 6. 提交与文件索引

- 关键提交：`d9ac066` spec、`a882e5a` plan、`dbbb530` contracts、`5b31570` 迁移/agent/settings/evidence/version/codeintel foundation、`b7c9211` fixtures、`6ea86e7` frontend、`0deb28b` visual/executor、`a34843a` codeintel core/impact、`eb0be72` tools、`bd6adab` wiring、`9c139e8`/`6f25933`/`b51b2cd`/`14bdeb2`/`a1bb983`/`ff47979`/`990608b`/`7b51386`/`e98f6e7`/`dcd12f9` 验收修复、`9c0b875`/`bcfbaf2` E2E scripts/format。
- 新增：`backend/internal/codeintel/**`、`backend/internal/visual/**`、`backend/cmd/eztest/p5_wiring.go`、`frontend/src/features/code-intel/**`、`frontend/src/features/visual/**`、`frontend/src/features/anchors/**`、`scripts/{fixture-p5-repo.ps1,fixture-p5-target.cjs,p5-acceptance.ps1,p5-e2e.ps1,p5-contract-check.cjs}`、`frontend/e2e/p5.spec.ts`。
- 修改（共享）：`backend/internal/server/server.go`、`backend/cmd/eztest/main.go`、`backend/internal/agent/**`、`backend/internal/execution/**`、`backend/internal/resource/**`、`frontend/src/features/{versions,execution,design,delivery,clarification,resources}/**`、`frontend/src/lib/api-client.ts`、`frontend/src/app/(app)/quality/page.tsx`、contracts 8 文件、`scripts/p4-contract-check.cjs`（前向兼容冻结标记）。
- 用户脏文件未提交：`docs/development.md`、`docs/startup.md`、`docs/用户专用文件，禁止AI阅读.md`、`scripts/dev-all.ps1`。

## 7. P6 输入与风险

**P6 输入**
- 8 个 codeintel 表 + 3 个 visual 表；`version_sut.code_baseline_commit`；`code` team/`index.repo`/`code.analyze`；`code.*` 只读工具；`execution.visual_assert`；`sut.page_propose`；12 事件；16 设置；10 evidence kind。
- P5 验收证据与真实 fixture：`.tmp/p5-acceptance/final7.log`、`scenarios.txt`、`.tmp/p5-repo`、`.tmp/p5-acceptance/{s4-design-agent-response.txt,s12-manual-baseline-check.txt}`。
- 前端可复用：CodeTab 深链、VisualView、QualityMetricsPanel、AnchorPanel、ProposalPanel、`useCodeEvents`。

**P6/P7/P8 风险**
1. **文档引用漂移**：phase-06 引用“D54–D58”在 decision-log 中不存在；phase-07 `D32/D33` 引用可能错位。P6 开始前必须从 spec 重建并核对，不得把 phase-06 的 2.3 KB 当详细设计。
2. **真实 LLM 抖动**：设计/执行 agent 偶发不调用指定工具（S4/S8）；P6 的 parity/search/agent-center 同样依赖工具调用，需要重试/最终器/可观测性。
3. **代码事实质量**：T1 fact 由真实 LLM 抽取，fixture 证据校验通过但跨 repo/大文件需限额/降级；`code_fact` 无 meta 列。
4. **多 repo union**：同 subject 跨 repo 合并以最高优先级来源元数据为准；PATCH 只对最佳候选 ID 生效，UI 需展示 repo/SUT 标签。
5. **视觉基线资产**：baseline 审批/环境指纹/config revision 语义；E2E harness 已修（`$script:apiProcess` + 端口守卫），真实 LLM 工具调用仍需重试兜底。
6. **profile 旧库 seed**：P5 增量不覆盖管理员编辑；升级库需迁移/管理界面补齐。
7. **`code.analysis_finished` candidates=0**：P6 若消费该字段会误判；用 `code.impact_computed`/impact API。
8. **E2E 已绿但依赖真实 LLM**：P5 浏览器 E2E 2/2 PASS（11 截图）；P6 验收仍需保留真实 LLM 重试/超时与 token/端口守卫，不能假设执行 Agent 每次都调用指定工具。

## 8. 停止声明

P5 在本分支交付完成：API/DB 真实 LLM + Chrome 验收 13/13 PASS，浏览器 E2E 2/2 PASS，全量门禁绿，契约冻结，handoff 完成。**未开始 P6**；等待用户验收/指示后再进入 P6。
