# EzTest 0.2 实施阶段计划

> 状态：**已批准**（2026-10-31）。按 P0–P8 串行阶段执行；每个阶段一个新会话，阶段末写 handoff 并等待验收。

> **For agentic workers:** 每个阶段开一个新会话；阶段开始前按顶部提示词读取设计文档、阶段文件、上一阶段 handoff 与 contracts。阶段内用 `writing-plans` 拆任务、TDD 执行；阶段结束必须更新 handoff 并停下等待用户验收，不自动进入下一阶段。

**Goal:** 按能力波次把 EzTest 0.2 从设计落到可运行的完整替代版；每个阶段产出可独立演示、可回归的软件与交接物。

**Architecture:** 采用“平台底座 → 控制面/澄清 → 设计 → 执行 → 交付/链路 → 全功能补齐 → P9 → 硬化验收”的依赖顺序；跨阶段只通过冻结契约、handoff、迁移与测试证据交互，不通过口头假设。

**Tech Stack:** Go + PostgreSQL + Next.js/React + Node/Playwright executor + 独立 headless 渲染 worker。

**Design sources（权威顺序）：** `docs/version-V0.2/architecture-design.md`、`docs/version-V0.2/high-level-design.md`、`docs/version-V0.2/research/decision-log.md`、`docs/version-V0.2/research/open-decisions.md`、`docs/version-V0.2/research/capability-matrix.md`、`docs/version-V0.2/ui/mock.html`。

---

## 0. 新会话启动提示词（每个阶段复制一次）

> 用法：把 `<PHASE_ID>`、`<PHASE_FILE>`、`<PREV_HANDOFF>`、`<PHASE_HANDOFF>` 替换为下方阶段表中的值；`<PHASE_HANDOFF>` 为本阶段要写的 `phase-XX-handoff.md`；首阶段 `<PREV_HANDOFF>` 写“无（本阶段无前置 handoff）”。

```text
你是 EzTest 0.2 的实施工程师。本会话只实施一个阶段：<PHASE_ID>。

开始前按顺序完整读取：
1. Agent.md
2. docs/version-V0.2/README.md
3. docs/version-V0.2/architecture-design.md
4. docs/version-V0.2/high-level-design.md
5. docs/version-V0.2/research/decision-log.md
6. docs/version-V0.2/research/capability-matrix.md
7. docs/version-V0.2/research/open-decisions.md
8. docs/version-V0.2/implementation-phase-plan.md
9. docs/version-V0.2/phases/<PHASE_FILE>
10. docs/version-V0.2/phases/<PREV_HANDOFF>（首阶段写“无（本阶段无前置 handoff）”，跳过读取）
11. docs/version-V0.2/contracts/ 下全部文件（若存在）
12. docs/version-V0.2/ui/mock.html

要求：
- 只实施本阶段范围；后续阶段内容只记录到 handoff/backlog，不实现、不提前改契约。
- 设计文档、决策日志、参数默认是权威；发现冲突、缺失或阶段前置不满足时，停止并报告，不自行改设计。
- 先按 Superpowers `writing-plans` 产出本阶段任务计划并自审，再执行；使用 TDD；每任务小步提交。
- 不修改 docs/version-V0.2 下的设计文档；只更新本阶段 handoff、contracts、迁移与测试证据。
- 阶段结束必须：测试通过、验收清单完成、更新 `docs/version-V0.2/phases/<PHASE_HANDOFF>`（如 `phase-00-handoff.md`）、列出下一阶段输入/风险；然后停止并报告，不自动开始下一阶段。
- 不评估人天；按可验证产物与任务依赖推进。
- 不要恢复 0.1 的旧控制面假设；以设计文档为准，发现相似旧模式时先确认。
```

### 阶段复制表

| 阶段 | PHASE_ID | PHASE_FILE | PREV_HANDOFF | PHASE_HANDOFF |
|---|---|---|---|---|
| P0 | P0 | phase-00-platform-foundation.md | 无 | phase-00-handoff.md |
| P1 | P1 | phase-01-runtime-clarification.md | phase-00-handoff.md | phase-01-handoff.md |
| P2 | P2 | phase-02-design.md | phase-01-handoff.md | phase-02-handoff.md |
| P3 | P3 | phase-03-execution.md | phase-02-handoff.md | phase-03-handoff.md |
| P4 | P4 | phase-04-delivery-chain.md | phase-03-handoff.md | phase-04-handoff.md |
| P5 | P5 | phase-05-codeintel-visual.md | phase-04-handoff.md | phase-05-handoff.md |
| P6 | P6 | phase-06-parity-quality-search-agent-center.md | phase-05-handoff.md | phase-06-handoff.md |
| P7 | P7 | phase-07-p9.md | phase-06-handoff.md | phase-07-handoff.md |
| P8 | P8 | phase-08-hardening-acceptance.md | phase-07-handoff.md | phase-08-handoff.md |

## Global Constraints

- 设计文档、决策日志、参数默认是唯一权威；实现不得自行改设计/字段/状态机。
- `.env` 严格 4 项；其余配置进 DB 设置中心。
- 0.1 数据不做原地迁移；不承诺接口/数据兼容。
- 每个会话只做一个阶段；阶段内先计划后实现；阶段结束写 handoff 并停止。
- 阶段间只通过冻结 contracts + handoff + 测试证据交互；破坏性变更需用户批准。
- 开发测试信任模型：测试账号密码 AI 可读；不做生产级安全加固/密钥托管。
- 所有阶段必须可独立演示、可回归；不把“功能写完”当验收。

---

## 1. 工作量评估（相对 WU，不是人天）

**WU 定义**：1 WU ≈ 一个可独立验收的能力切片（含后端/前端/迁移/测试/契约更新），只用于比较复杂度和排序，**不代表人天、小时或日历时间**。AI 开发下，真正成本在契约冻结、跨阶段集成、真实 LLM/E2E/多 SUT/多副本验收，而不是代码行数。

### 1.1 工作量画像

| 维度 | 规模 |
|---|---|
| 阶段数 | 9（P0–P8），串行阶段、阶段内可并行子任务 |
| 后端限界上下文/模块 | 15+（platform/settings/identity/llm/eventing/storage/audit/resource/version/design/execution/delivery/knowledge/agent/orchestration/chain/codeintel/visual/quality/search/workspace） |
| 数据库表 | 约 60–70 张（含任务板/上下文/版本/用例/执行/交付/P9） |
| API 分组 | 15+ 组、150+ 操作（含内部执行器/设置/导出） |
| 前端 feature | 15+（shell/versions/clarification/design/execution/chain/delivery/knowledge/resources/quality/agent-center/settings/code-intel/visual/search/todos） |
| Agent/工具 | 4 团队 + 15 左右角色；60+ 工具/提交 schema |
| 外部集成 | LLM 网关、Playwright executor、独立报告渲染 worker、PostgreSQL |
| P9 项 | 5（报告 PDF/模板、跨版本知识、GC、多实例、问题重开/合并/去重） |
| 验收面 | 单测/集成/E2E/fake LLM/真实 LLM/多 SUT/长上下文/多副本/P9 |

### 1.2 阶段 WU 与复杂度

| 阶段 | 复杂度 | WU | 主要工作量来源 | 主要风险 |
|---|---|---|---|---|
| P0 平台底座 | XL | 8 | 设置中心/资源模型/多 SUT 页面 API/存储账本/契约骨架/壳 | 资源模型与后续阶段契约 |
| P1 运行时+澄清 | XL | 9 | 任务板/租约/Agent runtime/上下文/对话/需求版本 | 自主性与上下文正确性 |
| P2 设计 | XL | 9 | 场景/用例版本/设计团队/脑图/确认/D25 | 图编辑并发与实时渲染 |
| P3 执行 | XL | 9 | intent/波次/多 SUT 执行器/证据/失败覆盖 | 跨系统执行与幂等 |
| P4 交付+链路 | L | 6 | 版本内问题/报告 HTML/链路读模型 | 报告 revision 与链路一致性 |
| P5 代码智能+视觉 | XL | 8 | 索引/diff/影响/锚点 + 视觉基线/差异 | 全功能对齐回归 |
| P6 全功能补齐 | L | 6 | 质量/搜索/待办/Agent 中心/链路增强 | 入口分散、验收面 |
| P7 P9 | XL | 8 | PDF/模板/知识/GC/多实例/问题合并 | D67 多副本、误删、报告一致性 |
| P8 硬化验收 | L | 6 | 全量 E2E/真实 LLM/多 SUT/多副本/SLO | 范围大、跨阶段回归 |
| **合计** | — | **69** | — | — |

> 结论：这是一个**大型完整替代版**，不建议压缩到 4–5 个阶段；P0/P1/P2/P3/P7 是五个高风险阶段，P8 必须独立存在。

---

## 2. 依赖分析

### 2.1 依赖图

```text
P0 平台底座/契约
 └─> P1 运行时 + 澄清
      ├─> P2 设计
      │    └─> P3 执行
      │         ├─> P4 交付 + 链路
      │         │    ├─> P5 代码智能 + 视觉
      │         │    │    └─> P6 全功能补齐
      │         │    │         └─> P7 P9
      │         │    └─> P7 P9（D59/D65/问题）
      │         └─> P5（视觉依赖执行）
      └─> P7（D67 多副本原语在 P1，编排在 P7）
P4/P5/P6 ─> P8 硬化验收
```

### 2.2 关键依赖

| 依赖 | 说明 |
|---|---|
| P1 → P2 | 需求版本/确认/对话/任务板/上下文是设计输入 |
| P2 → P3 | 已确认用例版本、版本绑定、SUT/页面/API 目标 |
| P3 → P4 | 波次/结果/证据是问题/报告/链路的数据源 |
| P4 → P5 | 报告/问题/代码仓/用例锚点是代码智能与视觉的集成面 |
| P3/P5 → P6 | 质量/搜索/待办/Agent 中心依赖执行/交付/代码/视觉产物 |
| P4/P5/P6 → P7 | D59 依赖报告 revision；D63 依赖知识/澄清/设计；D65 依赖 content ledger 与全域对象；D67 依赖 P1 租约/事件；问题合并依赖 P4 |
| P0/P1 → P7 D67 | lease/fencing/PG NOTIFY/任务板在 P0/P1 就要按多副本设计，P7 做部署编排 |
| 所有 → P8 | 全功能对齐 + P9 + 硬化 |

### 2.3 并行建议

- **不要并行两个阶段会话**：阶段间契约尚未冻结时并行会产生返工。
- 阶段内可并行子代理：按写作用域拆分（后端模块/前端 feature/测试/文档），Lead 统一评审与合并。
- P5 与 P6 在 P4 完成后理论上可部分并行，但为控制契约漂移，建议顺序执行；若要并行，必须先冻结代码/视觉/质量/搜索的只读接口。
- P7 的 D67 多副本可与 D59/D63 部分并行，但必须由同一阶段会话统一收口。

---

## 3. 阶段定义

### P0：平台底座与契约

**目标**：建立 0.2 的新地基——设置中心、身份、LLM、事件/SSE、存储账本、审计、资源（SUT/环境/页面/API/库/账号/代码仓）、版本与多 SUT 绑定、前端壳与 IA、迁移/测试骨架、contracts 骨架。

**关键交付**
- 4 项启动 env + DB 设置中心 + 管理员设置 UI。
- SUT/环境/页面/API/DB/测试账号/代码仓 CRUD；版本创建与多 SUT 绑定（环境、页面/API scope、库、账号、代码仓）。
- 内容对象/引用账本；事件/SSE；审计；身份/登录。
- 契约骨架：OpenAPI、事件目录、DB 迁移顺序、设置键、任务/工具类型占位。
- fake LLM 与测试骨架；空壳版本工作台。

**退出标准**：管理员可配置资源；任意用户可建多 SUT 版本并查看空工作台；设置中心生效；事件流可回放；迁移可重复；契约骨架冻结；测试通过；handoff 完成。

**不做**：任务板/Agent runtime/对话/需求/设计/执行（P1+）。

### P1：控制面 + 澄清

**目标**：建立共享任务板/租约/reconciler、Agent runtime/上下文、团队频道；打通“新增对话 → AI 澄清 → 需求版本 → 用户确认/回退”的垂直闭环。

**关键交付**
- work_item/lease/fencing/reconciler；团队频道/摘要；agent profile/run/context packet；工具作用域注入（不再让模型传 versionId）。
- conversation/message/summary；requirement/requirement_version/requirement_confirmation/relation；附件解析（doc/pdf/md/ppt）。
- 澄清 Host/Curator/Scout；需求列表/编辑器/批量确认/回退卡；上下文分层与压缩；长会话 stress 测试。
- 版本状态投影 initial/running；设计待办 CTA 的占位（P2 完善）。

**退出标准**：用户新建对话 → AI 读上下文/附件 → 产出 draft 需求 → 用户编辑/单条/批量确认 → AI 可回退 → 关联用例 stale 规则可验证；任务板可恢复/去重；context_overflow 不再判失败；测试通过；handoff 完成。

**不做**：设计/脑图/用例（P2）、执行（P3）。

### P2：设计

**目标**：打通“新建设计对话 → 设计团队扫描待设计需求 → 实时脑图 → 用户对话/确认”的垂直闭环。

**关键交付**
- scenario/case_entity/case_version/step/assertion/confirmation/source；design_round/design_scope；D25 A+ 开关。
- Design Host/Planner/Author/Critic/Data Planner；结构化图提交 + 服务端校验；图增量事件/实时渲染。
- 需求→场景→用例横向脑图；用例详情；逐用例/场景/需求确认；用例导出（Markdown/Mermaid/OPML/CSV）。
- 需求版本变化→case stale；新 draft→旧 confirmed superseded 规则；设计待办 CTA。

**退出标准**：新建设计对话 → 团队扫描 → 初版脑图 → 对话修改 → 确认 → 可导出；跨 SUT 用例卡片可表达；stale/重确认可验证；测试通过；handoff 完成。

**不做**：执行/波次（P3）。

### P3：执行

**目标**：打通“已确认用例 → execution_intent → 自动波次 → 多 SUT 执行 → 证据/四态结果 → 失败/覆盖分析”的闭环。

**关键交付**
- execution_intent/run_wave/case_run/case_run_sut_context/step_result/assertion_result/evidence；单活跃波唯一约束（含 paused）；intent 取消/重跑。
- Execution Planner/Case Executor/Failure Analyst/Coverage Analyst；executor 注册/心跳/容量/排空/fencing；多 SUT 浏览器上下文；账号角色解析；页面/API 目标路由；DB 断言。
- 执行 UI（波次/实时/结果/证据/暂停恢复）；登录接管 human_request；执行器离线重派。
- 失败分类/问题候选；覆盖缺口→design.gap（设计侧在 P2 已具备）。

**退出标准**：确认用例自动入队/组波；单 SUT 与跨系统用例均能执行；证据/四态结果/暂停恢复/重派可验证；失败/缺口任务产生；测试通过；handoff 完成。

**不做**：问题单/报告/链路（P4）。

### P4：交付 + 链路

**目标**：打通“结果 → 问题 → 波次/版本报告 → 探索链路”的交付闭环。

**关键交付**
- issue/issue_ref/status_history/analysis_run；问题确认/驳回/基础合并；报告 report/report_revision（HTML/revision/as-of/stale）；波次/版本报告；版本内问题/报告 Tab。
- chain 读模型 + 探索链路 UI（需求→用例→执行→证据/问题→报告→闭环）；SUT/页面/波次过滤、实时、回放、深链。
- 版本收尾 Version Steward + close 谓词/report.version；重开扫描。

**退出标准**：波次结束自动问题/报告；链路实时更新并能追溯闭环；版本可收尾/重开；测试通过；handoff 完成。

**不做**：PDF/模板（P7）、知识跨版本（P7）、代码/视觉（P5）。

### P5：代码智能 + 视觉断言

**目标**：补齐 0.1 P0–P8 的差异化能力：代码索引/diff/影响/锚点/事实复核，以及视觉基线/审批/差异。

**关键交付**
- code_index_run/entity/edge/fact/analysis/impact/anchor/fact_review；Git 索引/diff；影响候选；代码锚点；事实人工修正；代码与变更 UI。
- visual_baseline/assertion/diff；基线审批；actual/diff 证据；四态分类；执行 Tab 的视觉子视图 + 质量中心指标。
- 设计/执行 Agent 工具接入代码事实与视觉。

**退出标准**：设计能读代码事实；影响分析可用；问题带代码锚点；视觉基线/审批/差异/证据可验证；测试通过；handoff 完成。

**不做**：P9（P7）。

### P6：全功能补齐

**目标**：补齐 P0–P8 剩余能力：质量/评测、全局搜索、待办中心、Agent 中心、链路增强与全局中心完善。

**关键交付**
- quality/eval run/metric/quality UI；搜索跨实体；待办（评审/接管/设计待办/人工请求）；Agent 中心（目录/运行/质量）；全局问题/报告中心完善；链路 v2 增强。
- 全功能对齐矩阵逐项有入口/验收。

**退出标准**：P0–P8 能力矩阵逐项可演示；测试通过；handoff 完成。

**不做**：P9（P7）。

### P7：P9

**目标**：补齐 0.2 的 P9 五项：D59 报告 PDF/模板、D63 跨版本知识、D65 GC、D67 多实例、问题重开/合并/跨波次去重。

**关键交付**
- D59：report_revision HTML/PDF + 独立 headless render worker + 模板/章节开关 + 重试。
- D63：global/system knowledge、显式晋升、审核、注入、撤回/复审。
- D65：统一 content ledger GC（附件/导出/报告/孤儿）、dry-run、引用保护、保留期、审计。
- D67：多副本安全（lease/fencing、PG LISTEN/NOTIFY、共享存储、readiness/排空/滚动升级、执行器注册/重派）。
- 问题：reopen/merge/alias/跨波次去重；旧报告 as-of。

**退出标准**：P9 五项独立验收；多副本故障演练；GC dry-run/引用保护；报告 PDF；知识晋升/注入；问题重开/合并/去重；测试通过；handoff 完成。

**不做**：0.3 新能力（多用户协作、非 PG、外部工单等）。

### P8：硬化与验收

**目标**：把 0.2 从“功能齐”推到“可交付”：全量 E2E、真实 LLM、多 SUT、长上下文、多副本、P9、SLO/安全/打包/文档、缺陷修复。

**关键交付**
- 全量 E2E：管理员配置→版本→澄清→设计→确认→执行→问题/报告→链路→收尾→重开。
- 真实 LLM 验收；多 SUT/跨系统；长会话/长执行；多副本演练；P9 验收；性能/SLO；安全审计；打包/部署；文档同步。
- 0.1 P0–P8 能力矩阵逐项签字；遗留问题清单与风险。

**退出标准**：验收清单全绿；无 P0/P1 缺陷；能力矩阵签字；handoff/交付说明完成；用户验收通过。

---

## 4. 阶段间信息交互

### 4.1 四类交接物

| 类型 | 位置 | 写入 | 读取 | 规则 |
|---|---|---|---|---|
| **设计文档** | `docs/version-V0.2/*.md`、`research/*`、`ui/*` | 设计会话 | 所有阶段 | 只读；阶段不得修改；冲突停止报告 |
| **契约** | `docs/version-V0.2/contracts/` | 阶段实施者 | 下一阶段 | 阶段内更新，阶段末冻结；破坏性变更需用户批准 + 决策记录 |
| **阶段 handoff** | `docs/version-V0.2/phases/phase-XX-handoff.md` | 阶段实施者 | 下一阶段 | 阶段末必写；未写不得进入下一阶段 |
| **测试/验收证据** | `docs/version-V0.2/acceptance/` 或 handoff 引用 | 阶段实施者 | 下一阶段/用户 | 命令、输出、截图/报告路径；不得只写“已通过” |

### 4.2 契约目录

`contracts/` 至少维护：

- `openapi.yaml`：对外 REST/SSE 契约（阶段增量）。
- `events.md`：事件类型/envelope/作用域字段。
- `db-schema.md` / `migrations-order.md`：表、关键约束、迁移顺序。
- `task-types.md`：work_item 类型/去重键/状态。
- `agent-profiles-tools.md`：profile、工具、作用域注入、提交 schema。
- `settings.md`：env 4 项、DB 设置键/默认/热加载。
- `handoff-template.md`：handoff 模板。

**冻结规则**：
- 阶段末将本阶段对外接口写入 contracts 并标记 `frozen`。
- 下一阶段只能消费 frozen 契约；需要变更时，先停止、说明影响、更新 contracts + 决策记录，再继续。
- 不允许“代码先行、契约后补”。

### 4.3 阶段 handoff 模板

```markdown
# Phase XX Handoff

> 状态：completed / blocked / partial
> 日期：
> 阶段：<PHASE_ID>

## 1. 完成范围
- ...
## 2. 契约变更
- API/事件/表/任务/工具/设置：...
- 是否 frozen：...
## 3. 测试与验收证据
- 命令：
- 结果：
- 演示路径：
## 4. 已知缺口/偏差
- 缺口：
- 是否影响下一阶段：
- 决策记录引用：
## 5. 下一阶段输入
- 必须读取的契约/表/接口：
- 前置检查：
## 6. 风险
- ...
```

### 4.4 会话与门禁

1. 用户用顶部提示词开新会话，填入阶段 ID/文件/上一 handoff。
2. 阶段实施者先读设计/契约/handoff/phase 文件；检查前置；不满足则停止报告。
3. 用 `writing-plans` 产出本阶段任务计划；自审；再实现。
4. 阶段内 TDD、频繁提交；每任务有验证命令。
5. 阶段末跑验收；写 handoff；更新 contracts；停止。
6. 用户审阅 handoff/演示；批准后才开下一阶段。
7. 若阶段内发现设计冲突或范围蔓延：停止，记录到 handoff/决策日志，等待用户裁决；不自行改设计。

### 4.5 写作用域

- 阶段内多子代理时，按“后端模块 / 前端 feature / 迁移 / 测试 / 契约”拆写作用域，避免同文件并发。
- 阶段之间不并行写同一模块；下一阶段先读 handoff/契约再动。
- 共享文件（contracts、migrations-order、openapi）由阶段 Lead 统一收口，子代理不直接并发改。

---

## 5. Review Focus（最容易出问题的五类）

1. **契约漂移**：后续阶段按旧接口/旧表写，导致返工；每个 gate 检查 contracts/frozen 与迁移顺序。
2. **多 SUT 身份混用**：`sut_id`/`version_sut_id`/页面/API/账号/DB 混用；每个执行相关测试必须覆盖跨 SUT。
3. **上下文压缩丢事实**：长会话/长执行后决定/约束丢失；必须有摘要+钉住事实+可展开原文的测试。
4. **执行幂等/重跑/收尾**：intent 唯一键、lease/fencing、stale 取消、重复波次、completed 重开；必须有并发/崩溃/重派测试。
5. **旧假设回流**：8 状态机/整版冻结/单活跃批次/问题终态等 0.1 习惯被重新引入；每个阶段对照 decision-log 检查。

> 每个阶段的 phase 文件必须把以上五类映射到本阶段验收测试；P8 做跨阶段复核。阶段内任务计划（writing-plans）要为每一项指定测试，而不是只写“注意”。

---

## 6. 阶段文件索引

| 阶段 | 文件 |
|---|---|
| P0 | [phase-00-platform-foundation.md](phases/phase-00-platform-foundation.md) |
| P1 | [phase-01-runtime-clarification.md](phases/phase-01-runtime-clarification.md) |
| P2 | [phase-02-design.md](phases/phase-02-design.md) |
| P3 | [phase-03-execution.md](phases/phase-03-execution.md) |
| P4 | [phase-04-delivery-chain.md](phases/phase-04-delivery-chain.md) |
| P5 | [phase-05-codeintel-visual.md](phases/phase-05-codeintel-visual.md) |
| P6 | [phase-06-parity-quality-search-agent-center.md](phases/phase-06-parity-quality-search-agent-center.md) |
| P7 | [phase-07-p9.md](phases/phase-07-p9.md) |
| P8 | [phase-08-hardening-acceptance.md](phases/phase-08-hardening-acceptance.md) |

契约模板：[contracts/README.md](contracts/README.md) · [contracts/handoff-template.md](contracts/handoff-template.md)
