# EzTest 0.2 P4 设计（交付 + 链路）— 实施规格

> 状态：实施规格（implementation spec）；2026-11-01 已按用户逐项裁决与根因审计更新；待用户审阅后进入 `writing-plans`。
> 阶段：P4。阶段文件：`docs/version-V0.2/phases/phase-04-delivery-chain.md`。
> 前置 handoff：`docs/version-V0.2/phases/phase-03-handoff.md`（P3 completed）。
> 计划：`docs/superpowers/plans/2026-11-01-eztest-v02-p4-delivery-chain.md`（本规格批准后生成）。
> 本规格只实现 P4；P5+ 内容只进 handoff/backlog/契约保留位，不实现、不提前建表。
> 本规格不修改 `docs/version-V0.2/**` 设计文档；与设计文档的偏差按 §16 `P4-D` 记录并等待用户批准；冲突按权威顺序裁决。

---

## 0. 依据、阶段映射与前置检查

### 0.1 权威来源与阅读依据

**权威顺序**：`architecture-design.md` → `high-level-design.md` → `research/decision-log.md` → `research/open-decisions.md` → `research/capability-matrix.md` → `phases/phase-04-delivery-chain.md` → P0/P1/P2/P3 frozen `contracts/*` → P0/P1/P2/P3 handoff。

**本规格实际查阅（Lead + 只读子代理）**：

- `Agent.md`（全文；其 P1–P7 为 0.1 编号）。
- `docs/version-V0.2/README.md`；`architecture-design.md` §6.4–§6.5、§7、§8.6–§8.8、§12.1–§12.5、§15–§16；`high-level-design.md` §1、§2.2–§2.4、§3.1–§3.3、§6.6–§6.9、§7、§8、§12。
- `research/decision-log.md`（全文）、`research/open-decisions.md`（全文）、`research/capability-matrix.md`。
- `implementation-phase-plan.md`（全文）、`phases/phase-04-delivery-chain.md`、`phases/phase-03-handoff.md`（全文）、`phases/phase-00…phase-08`（边界）。
- `contracts/README.md`、`contracts/task-types.md`、`contracts/db-schema.md`、`contracts/events.md`、`contracts/settings.md`、`contracts/agent-profiles-tools.md`、`contracts/migrations-order.md`、`contracts/executor-protocol.md`、`contracts/openapi.yaml`（P4 相关路径逐段）。
- `ui/mock.html`、`ui/mock.css`、`ui/screenshot.cjs`；`ui/*.png` 未读（以 HTML/CSS 为准）。
- 既有 0.2 P0–P3 spec/plan/handoff（裁决与格式）。
- 现有实现：`backend/internal/{agent,resource,version,design,execution,storage,eventing,audit,settings,platform,db,server}`、`frontend/src/features/*`、`scripts/test.ps1`、`scripts/p2|p3-*.ps1|cjs`。
- 未读：`docs/用户专用文件，禁止AI阅读.md`（遵守禁止项）。

### 0.2 阶段映射与编号

- `implementation-phase-plan.md` §0：**P4 → `phase-04-delivery-chain.md`；`PREV_HANDOFF=phase-03-handoff.md`；本阶段写 `phase-04-handoff.md`**。
- **实施分支**：从 `feat/v0.2` @`5b1e326` 拉出 `feat/v0.2-p4`；完成后合回 `feat/v0.2`；保留阶段分支审计（P3 handoff §0 惯例）。
- `Agent.md` §6.1 的 P1–P7 是 **0.1 阶段编号**；0.2 的模块/上下文以本文 §2 与 `high-level-design.md` §1 为准。P4 不修改 `Agent.md`（记录在 handoff）。
- 既有工作树 dirty 文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1`、用户专用文件）不属于 P4；提交不纳入，禁止 `git add -A`。

### 0.3 前置检查（实施开始前执行；不满足则停止报告）

1. `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → EXIT=0（P3 基線）。日志 `.tmp/p4/baseline-test.log`。
2. `node scripts/p3-contract-check.cjs` 与 `node scripts/p2-contract-check.cjs` → PASS（P3 基線）。P4 阶段内把两者改为“增量仍在”的兼容模式（§14）。
3. 迁移基线 = 20；`order.txt` 末项 `execution_0003_executor_human.sql`。P4 只追加 #21–#24。
4. `.env` 有真实 DeepSeek 凭据（小写 `llm_api_key/llm_base_url/llm_model_name`）；API 进程仍只读 4 项 `EZTEST_*` env。
5. executor `127.0.0.1:13336` 可启动（Playwright 1.64.x）；验收由脚本启动，不依赖当前手工进程。
6. PostgreSQL 17.6 @5433 可用；开发库 `eztest_v02` 当前仅 8 迁移，迁移前先备份；验收使用隔离库 `eztest_v02_p4`。
7. 工作树 dirty 文件与用户专用文件不纳入提交。

### 0.4 用户裁决（2026-11-01，P4 会话）

| # | 裁决 | 说明 |
|---|---|---|
| U1 | **P4 纳入最小需求闭环** | `requirement_closure` 表 + `requirement.close` 任务 + `requirement.goal_met` 工具 + 服务端校验 + 需求关闭/重开路径；作为版本收尾前置 |
| U2 | **报告存储 = A（content ledger 权威）** | `html_ref=content_ref.id`；自动发布；旧 revision 保持 published/as-of；固定内置 HTML + 现有章节开关；不写 `paths.report_dir`；`report_ref` 建表；PDF 留 P7 |
| U3 | **问题 = A（P4 做基础重开）** | `confirmed/dismissed→open` + 基础合并；merged 重开、跨波次去重、AI 语义合并、refs 迁移留 P7 |
| U4 | **回放 = A（两档）** | SSE 实时补播 + 按波次/事件区间历史回看；不承诺任意时点 as-of；预算 300/600 + `truncated`；补历史 CaseReader |
| U5 | **实施方案 = A** | 契约先行 + 共享底座 + 并行工作流（W1 issue / W2 report / W3 closure+Steward / W4 chain / W5 frontend / W6 acceptance） |
| U6 | **真实 LLM 质量原则（用户 2026-11-01 纠正）** | 真实 LLM 稳定，代表最终交付质量；真实 LLM 失败是**系统缺陷信号**（上下文/提示词/工具/scope 设计问题），按根因修复后重跑；**验收脚本只用真实 LLM，不用 fake LLM 顶替**；fake 仅用于单测/集成的端口 stub，且不属验收 |

### 0.5 根因记录（为什么 P4 才发现缺口）

- `9173c52`（2026-10-31 16:59）一次性写入设计文档与 9 个 phase 文件（含 phase-04）；phase-04 全历史仅该 1 次提交，早于 P3 批准（2026-11-01 01:01）与 P3 裁决，此后从未回写。
- 主要缺口：**阶段范围遗漏**（P4 In 与 IPP P4 段漏 `requirement.close`/`closure`；P3 handoff 新任务清单也漏）；**契约预留无 owner**（`task-types.md:63-67` 的“P4+ 保留”不指定认领阶段，`p3-contract-check.cjs:325` 只断言仍保留）；**文档漂移/误引**（P1 handoff 把 closed/obsolete 写成已交付；P1 研究笔记误引 P4）；**门禁只验已实现增量**。
- 报告四口径（HLD `html_ref/pdf_ref`、ARCH content ledger、`export_artifact`、`paths.report_dir`）与 phase-04 L33“链路表”同生于基线；不是 P1–P3 实现删改。
- P3“不建 analysis_run/issue/report”是**阶段边界**（`2026-11-01-eztest-v02-p3-execution-design.md:9,60,869`；`phase-03-handoff.md:119`），与 P4 不冲突。
- 本轮结论进入 §16 `P4-D`、§18 handoff“未交付实体移交表 + 裁决索引”；P5+ 前置检查必须核对本表。

### 0.6 设计实体覆盖矩阵（P4 认领）

| HLD/ARCH 实体 | 设计出处 | Owner 阶段 | P4 交付 | 契约落点 |
|---|---|---|---|---|
| `issue` / `issue_ref` / `issue_status_history` | HLD §2.2 L198-200；ARCH L289 | **P4** | 表 + API + 事件 + UI | db-schema §10、openapi、events §13 |
| `analysis_run` | HLD §2.2 L201 | **P4** | 表；P4 写入方仅 `issue.triage` | db-schema §10、task-types §6 |
| `report` / `report_revision` / `report_ref` | HLD §2.2 L202-204；ARCH L290、§12.1 | **P4**（HTML） | 表 + 确定性 HTML + 发布/重生成 + UI | db-schema §10、openapi、events §13 |
| `report.render_pdf` / PDF | HLD §3.1 L279；ARCH §12.1 | P7 | 不做；`pdf_ref` 可空、不调度 | settings 注明 |
| `requirement_closure` / `requirement.close` / `goal_met` | HLD L155/L272/L470；ARCH L238/L249 | **P4** | 表 + 任务 + 工具 + 服务端校验 + 重开 | db-schema §10、task-types §6、profiles §7 |
| `version` close/reopen | HLD §6.7 L482-487；ARCH §8.7 | **P4** | 谓词 + Steward + 人工重开 API + 扫描 | openapi、events §13、task-types §6 |
| chain 读模型 | HLD L229（无表）、§6.8；ARCH §8.8 | **P4** | 只读 API + UI + 历史 reader | db-schema 无表、openapi、settings |
| 版本内 `human_request` 待办入口 | phase-04 L23；P3 human_request | **P4** | Topbar TodoDrawer（当前版本） | openapi 复用 P3 |
| 全局 `/api/todos` / 待办中心 | HLD §7.1 L529、§8.1 L564；phase-06 L242 | P6 | 不做 | handoff 移交 |
| 全局问题/报告中心 | HLD §7.1 L524；phase-06 | P6 | 不做 | handoff 移交 |
| issue 重开/合并增强、跨波次去重、alias | D32 L82、ARCH §12.5；phase-04 L28 | P7 | 只做基础 | handoff 移交 |
| report/chain `export_artifact` 扩展 | HLD L205/L520；P2H L151 | P6（chain）/P7（PDF） | 不做；报告 HTML 直下 | handoff 移交 |
| `requirement.obsolete` 写入路径 | HLD L247/L484 | P6 | 不做（P4 只读该状态） | handoff 移交 |
| chain 任意时点 as-of / 快照 | ARCH L429；D47 | P6/P7 | 不做（两档回放） | handoff 移交 |
| 知识/GC/多实例/PDF | phase-04 Out L27-28 | P7 | 不做 | handoff 移交 |
| 代码/视觉 | phase-04 Out L29 | P5 | 不做 | handoff 移交 |

### 0.7 不在 P4（显式 Out + owner）

- PDF/模板系统/渲染 worker/模板编辑器 → **P7**（D59）。
- 跨版本知识、统一 GC、多实例、问题重开/合并增强/跨波次去重 → **P7**。
- 全局问题/报告中心、全局待办中心、chain v2 任意 as-of、链路导出 → **P6**。
- 代码智能/视觉断言/质量/搜索/Agent 中心 → **P5/P6**。
- Excel 导出、通知/微信/工单/CI 集成 → 0.2 不做（decision-log §11）。
- `requirement.obsolete` 写路径 → **P6**（P4 仅在谓词/链路中读取该状态）。
- 历史 DB 约束名修复（33 内联 PK + 8 未命名 UNIQUE）→ **独立合规任务/backlog**（P4-D-16；P4 不执行，布尔字段不改名）。

### 0.8 P4-D 裁决索引

完整表见 §16；编号：`P4-D-01`…`P4-D-15`。实现与测试必须逐条可追溯；任何新增偏差先在 §16 追加并停下报告。

---

## 1. 目标与退出标准

### 1.1 目标

打通“结果 → 问题 → 波次/版本报告 → 探索链路 → 需求闭环 → 版本收尾/重开”的交付闭环；P4 结束时可在隔离环境用**真实 LLM** 从一次失败执行走到版本 `completed`，再因新目标自动回到 `running`，并有可回看的链路、问题与报告。

### 1.2 退出标准（逐条可验证）

1. 波次终态自动产生问题候选（仅 `failed(application)`），问题可确认/驳回/基础合并/基础重开；同 dedupe_key 跨波次幂等。
2. 波次终态自动生成 HTML 报告并发布；`report_revision` append-only、as-of/input_fingerprint 可验；旧 revision 保持 published 且 HTML 字节不变。
3. 版本总结报告在收尾前发布；输入变化可标 stale 并重生成新 revision。
4. 探索链路实时更新，支持 SUT/页面/波次/状态/结果过滤、深链、实时跟随、按波次/事件区间历史回看、预算截断提示；superseded 用例标题可读。
5. 所有需求经 `requirement.close` 服务端校验后 closed；失败应用用例有 issue 引用；open issue 不阻断闭环。
6. 版本收尾谓词满足时 Version Steward 通过 `version.close_propose` 关闭版本（completed/closed_at/close_reason/close_run_id）；新需求/需求重开/新确认/新 intent/人工重开自动或手动回 running。
7. 版本内 open `human_request` 有待办入口可看可跳转；resolved 后消失。
8. 真实 LLM 验收全绿、浏览器 E2E 与截图齐全；`scripts/test.ps1`、P4 契约检查、迁移幂等、P1/P2 回归通过；contracts 冻结 `P4`；`phase-04-handoff.md` 完成；不自动进入 P5。

### 1.3 演示路径（P4 handoff 必须引用）

管理员配置 → 建版本/绑 SUT → 需求澄清/确认 → 设计/确认用例 → 执行产生失败 →（P4）问题候选/确认 → wave 报告 → 需求闭环 → version 报告 → Steward 收尾 → 链路回看 → 新需求/人工重开 → running。

---

## 2. 实施策略、模块边界与仓库约束

### 2.1 模块与目录（按 HLD §1.1）

```text
backend/internal/
├─ delivery/                    # 新限界上下文：问题/报告/分析（0.2 P4）
│  ├─ domain/                   # issue/issue_ref/history/report/revision/report_ref/analysis_run、常量、错误
│  ├─ application/              # ingest/issue/report/render/publish/analysis 服务、ports
│  ├─ infrastructure/           # {postgres,content,eventing,agent,audit,version,execution,design} 适配
│  └─ interfaces/httpapi/       # /api/versions/{id}/{issues,reports} handler、DTO
├─ chain/                       # 新限界上下文：探索链路只读读模型（0.2 P4）
│  ├─ domain/                   # node/edge/view/filter/budget 类型
│  ├─ application/              # chain 服务、provider ports
│  ├─ infrastructure/           # 各上下文 provider 适配（只调用 application）
│  └─ interfaces/httpapi/       # /api/versions/{id}/exploration-chain
├─ version/                     # 扩展：requirement_closure、需求关闭/重开、版本 close/reopen、Steward 工具
├─ agent/                       # 扩展：profiles、work type→profile、reconciler 规则、工具注册、seed
├─ db/migrations/               # #21–#24
└─ server/ + cmd/eztest/        # 路由/装配（Lead 收口）
frontend/src/features/
├─ delivery/                    # 问题 Tab、报告 Tab（版本内）
└─ chain/                       # 探索链路 Tab
```

- 每个上下文四层齐全；导入方向遵守 `Agent.md` §6.2。跨上下文只依赖对方 `application` 接口；`chain` 与 `agent` 使用 consumer-side port，适配器在 `cmd/eztest` 或本上下文 `infrastructure`。
- `delivery` 不直接读 `version/design/execution` 表；通过 application 端口取数。`chain` 只读，不写业务表。
- HTML 渲染在 `delivery/infrastructure/render` 内，用 Go `html/template`；不新建独立 renderer 进程。
- 新增常驻 worker/reconciler 规则由 `cmd/eztest` 装配并随进程启停；单实例假设写明。

### 2.2 共享文件与写作用域

**Lead 串行收口（子代理禁止并发写）**：`docs/version-V0.2/contracts/*`、`backend/internal/db/migrations/*`（含 `order.txt`）、`backend/cmd/eztest/main.go`、`backend/internal/server/*`、`backend/internal/agent` 的 profile seed/工具注册/worker 映射、`scripts/test.ps1`、`scripts/*contract-check*`、`docs/version-V0.2/phases/phase-04-handoff.md`。

**并行写作用域（见 §15）**：W1 `delivery` issue 前缀文件；W2 `delivery` report 前缀文件；W3 `version` closure/close 与 `agent` steward 相关文件；W4 `chain` 全上下文；W5 `frontend/src/features/{delivery,chain,versions,shell}`；W6 `scripts/p4-*`、`frontend/e2e/p4.spec.ts`、fixture 扩展、`.tmp/p4-acceptance/*`。
W1/W2 共享 `delivery/domain/types.go` 由 Lead 先落并冻结；W3 依赖端口而非 W1/W2 实现；W5 只依赖 OpenAPI。

### 2.3 契约先行顺序

1. 写 contracts 增量（db-schema §10、events §13、openapi `0.2.0-p4`、task-types §6、profiles §7、settings §4d、migrations-order §6、contracts README P4 冻结状态）。
2. 写迁移 #21–#24 + `order.txt`。
3. 落端口/类型/路由/seed 骨架 + 前向兼容门禁（p2/p3 检查、migrate 测试）。
4. 并行实现 W1–W6。
5. Lead 集成、真实 LLM 验收、冻结 contracts `P4`、写 handoff。

禁止“代码先行、契约后补”；破坏性变更需先停止并更新契约/裁决/handoff。

### 2.4 真实 LLM 原则（全阶段强制）

- Agent 行为（Issue Triager、Report Writer、Version Steward，以及 P3 Case Executor 的集成烟测）必须以**真实 LLM** 验收；验收脚本不启用 fake LLM 上游。
- 真实 LLM 失败先按系统缺陷定位：上下文包是否缺失/错误、工具 scope/schema 是否让模型无法正确调用、提示词是否含歧义、服务端校验是否拒绝合法输出、超时/预算是否设置错误。修复后重跑。
- 只有外部网络/供应商短时不可用可记录 blocked 并重试；**不得**以 fake 结果替代，不得把行为失败标为“模型不稳定”后跳过。
- 单测/集成可用 fake port（不调用真实 LLM）；这些测试不构成验收，不写入验收证据表。

### 2.5 代码规范自检（P4 强制）

P4 新代码/迁移必须满足 `Agent.md` §6.2–§6.9；Lead 在每个工作流合并前检查：

| 项 | 要求 | 检查方式 |
|---|---|---|
| 目录/四层 | 新上下文 `delivery`/`chain` 四层齐全；不建 utils/common/helpers | 目录审查 |
| 导入方向 | domain 只依赖标准库/platform；application 不导入 infrastructure/interfaces；跨上下文只依赖 application 端口 | `grep` + `go build ./...` |
| SQL 位置 | 只在 infrastructure/postgres；handler 不写 SQL、不调 LLM SDK | 代码审查 |
| ID | 新表主键 `id BIGINT` 雪花；JSON/工具/URL 中 ID 十进制字符串；禁止 BIGSERIAL/SERIAL | 迁移审查 + 测试 |
| DB 命名 | 按 §3.7：`delivery_*` 表、`pk_/uk_/idx_`（全列名）、`created_at/updated_at`、同上下文 FK、跨上下文逻辑引用 | `p4-contract-check` + 迁移审查 |
| 文件存储 | 报告 HTML 走 content ledger；二进制不入 PG；不写 `paths.report_dir` | 集成测试 |
| 设置/env | API 只读 4 项 `EZTEST_*` env；新配置进 DB settings | config 审查 |
| 前端 | `src/app` 只做路由/组装；请求只走 api-client、SSE 只走 sse-client；feature 结构；共享 UI 在 components/ui；不建 utils 大杂烩 | `grep` + typecheck/lint |
| 测试 | 与代码同包/同目录；集成测试放 `internal/<context>/integration` 或 `_test.go`；不把测试辅助代码放进生产包 | 目录审查 |
| 格式化 | `gofmt -l` 为空；`go vet ./...`；`pnpm typecheck`/`pnpm lint` 通过 | 门禁命令 |

**既有 P0–P3 代码审计结论（只读，2026-11-01）**：

- 干净项：无 `BIGSERIAL/SERIAL`；domain/application 未反向导入 infrastructure/interfaces；跨上下文无 DB FK；前端仅 `api-client`/`sse-client` 使用 `fetch`；env 仅在 config/testutil 读取；P3 evidence 不可变表的 `updated_at` 例外已有文档。
- 既有偏差（**不在 P4 修改已冻结迁移**，记录 handoff backlog）：33 个表使用内联 `id BIGINT PRIMARY KEY`（PG 自动名 `<table>_pkey`，非 `pk_<table>`）；8 个未命名 `UNIQUE`（自动名非 `uk_*`）；7 个布尔列无 `is_` 前缀（`enabled/supports_tools/supports_vision/read_only/restart_required/auto_design_new_requirements/selected`）；部分表名/索引名为历史泛名。
- 布尔字段/既有字段改名会破坏既有契约与字段名约定（D34 §6.9.2），P4 不改；若用户要求约束名修复，需追加 metadata-only 重命名迁移（跨上下文，超出 P4 四迁移范围），作为独立 `P4-D-16` 由用户批准后执行。
- P4 不复制上述偏差；§3.7 的新表命名/索引/审计列为准。

---

## 3. 数据模型与迁移（只追加 #21–#24）

### 3.1 通用规则

- 主键 `BIGINT` 雪花，应用层生成（D29）；JSON/工具/API 中 64-bit ID 一律十进制字符串。
- 跨上下文只做逻辑引用，不建 FK；同上下文可建 FK（`ON DELETE CASCADE` 仅限本规格明确处）。
- 审计列：**所有 P4 新表** `created_at/updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`（D34 §6.9.4）；不可变/append-only 表的 `updated_at` 仅插入时初始化，仓储不提供更新方法，不可变语义由接口/测试保证（与 P3 evidence 的“仅 created_at”先例相比，P4 取更严格的 D34 字面口径）。
- 迁移命名 `<context>_<NNNN>_<desc>.sql`；只追加，不改 #1–#20；`order.txt` 追加末尾；checksum 基于原文，无 down。

### 3.2 `delivery_0001_issue_lifecycle.sql`（order #21，delivery 上下文，3 表）

> D34 映射：HLD 字面 `issue→delivery_issue`、`issue_ref→delivery_issue_ref`、`issue_status_history→delivery_issue_status_history`；API/事件/DTO 仍用 `Issue*`，只映射 DB 表名。

**`delivery_issue`**

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_issue`（雪花） |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `title` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 500` |
| `summary` | TEXT | NOT NULL DEFAULT ''，CHECK `char_length <= 4000` |
| `status` | TEXT | NOT NULL DEFAULT 'open'，CHECK `open/confirmed/dismissed/merged` |
| `category` | TEXT | NOT NULL，CHECK `ui/api/db` |
| `severity` | TEXT | NOT NULL，CHECK `major/minor/trivial` |
| `dedupe_key` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{32,64}$'` |
| `fingerprint` | TEXT | NOT NULL DEFAULT ''，CHECK `= '' OR ~ '^[0-9a-f]{64}$'` |
| `source` | TEXT | NOT NULL DEFAULT 'failure_candidate'，CHECK `failure_candidate` |
| `expected_revision` | INTEGER | NOT NULL DEFAULT 1，CHECK `> 0`（CAS） |
| `merged_into_id` | BIGINT | NULL，同上下文 FK `REFERENCES delivery_issue(id)`（合并目标；禁自指由 CHECK 保证） |
| `merged_at` | TIMESTAMPTZ | NULL |
| `merged_by` | BIGINT | NULL（逻辑引用 identity） |
| `reopen_count` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run；确定性创建为 NULL） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_delivery_issue`；`uk_delivery_issue_version_id_dedupe_key UNIQUE (version_id, dedupe_key)`；`idx_delivery_issue_version_id_status_id (version_id, status, id DESC)`；`idx_delivery_issue_merged_into_id (merged_into_id)`；`idx_delivery_issue_created_at (created_at)`；CHECK `merged_into_id IS NULL OR merged_into_id <> id`。

**`delivery_issue_ref`**（append-only；`updated_at` 仅初始化）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_issue_ref` |
| `issue_id` | BIGINT | NOT NULL，FK `REFERENCES delivery_issue(id) ON DELETE CASCADE` |
| `kind` | TEXT | NOT NULL，CHECK `case_run/case_version/wave/step_result/assertion_result/evidence/report/requirement` |
| `ref_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `version_sut_id` | BIGINT | NULL（逻辑引用 version） |
| `role` | TEXT | NOT NULL DEFAULT 'source'，CHECK `char_length <= 32` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`uk_delivery_issue_ref_issue_id_kind_ref_id_role UNIQUE (issue_id, kind, ref_id, role)`；`idx_delivery_issue_ref_kind_ref_id (kind, ref_id)`；`idx_delivery_issue_ref_issue_id_id (issue_id, id)`。

**`delivery_issue_status_history`**（append-only；`updated_at` 仅初始化）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_issue_status_history` |
| `issue_id` | BIGINT | NOT NULL，FK `REFERENCES delivery_issue(id) ON DELETE CASCADE` |
| `from_status` | TEXT | NULL（创建时为 NULL），CHECK `NULL OR open/confirmed/dismissed/merged` |
| `to_status` | TEXT | NOT NULL，CHECK `open/confirmed/dismissed/merged` |
| `reason` | TEXT | NOT NULL DEFAULT ''，CHECK `char_length <= 2000` |
| `actor_id` | BIGINT | NULL（逻辑引用 identity） |
| `actor_kind` | TEXT | NOT NULL，CHECK `user/agent/system` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

索引：`idx_delivery_issue_status_history_issue_id_id (issue_id, id)`。

### 3.3 `delivery_0002_report_revision.sql`（order #22，delivery 上下文，3 表）

> D34 映射：HLD 字面 `report→delivery_report`、`report_revision→delivery_report_revision`、`report_ref→delivery_report_ref`；`content_ref.owner_kind` 仍用语义名 `report_revision`（与 evidence 的 owner_kind 用法一致）。

**`delivery_report`**

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_report` |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `wave_id` | BIGINT | NULL（逻辑引用 run_wave；kind=wave 必填） |
| `kind` | TEXT | NOT NULL，CHECK `wave/version` |
| `current_revision_id` | BIGINT | NULL（逻辑引用 delivery_report_revision） |
| `status` | TEXT | NOT NULL DEFAULT 'draft'，CHECK `draft/published/stale` |
| `input_fingerprint` | TEXT | NOT NULL DEFAULT ''，CHECK `= '' OR ~ '^[0-9a-f]{64}$'` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_delivery_report`；CHECK `(kind='version' AND wave_id IS NULL) OR (kind='wave' AND wave_id IS NOT NULL)`；partial unique `uk_delivery_report_version_id_wave_id (version_id, wave_id) WHERE kind='wave'`、`uk_delivery_report_version_id_kind (version_id) WHERE kind='version'`；`idx_delivery_report_version_id_kind_id (version_id, kind, id DESC)`；`idx_delivery_report_current_revision_id (current_revision_id)`。

**`delivery_report_revision`**（内容 append-only）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_report_revision` |
| `report_id` | BIGINT | NOT NULL，FK `REFERENCES delivery_report(id) ON DELETE CASCADE` |
| `revision` | INTEGER | NOT NULL，CHECK `> 0` |
| `status` | TEXT | NOT NULL DEFAULT 'draft'，CHECK `draft/published/stale` |
| `html_ref` | BIGINT | NULL（逻辑引用 `content_ref.id`；P4 必填后置 published） |
| `pdf_ref` | BIGINT | NULL（逻辑引用 `content_ref.id`；P4 不写，P7 回填） |
| `input_fingerprint` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{64}$'` |
| `as_of` | TIMESTAMPTZ | NOT NULL |
| `template_version` | TEXT | NOT NULL DEFAULT '1'，CHECK `char_length <= 32` |
| `render_status` | TEXT | NOT NULL DEFAULT 'pending'，CHECK `pending/running/done/failed` |
| `created_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`uk_delivery_report_revision_report_id_revision UNIQUE (report_id, revision)`；`idx_delivery_report_revision_report_id_revision (report_id, revision DESC)`；`idx_delivery_report_revision_html_ref_id (html_ref)`。
**append-only 约束**：`revision/status/html_ref/input_fingerprint/as_of/template_version` 在**发布落定后**不可变；创建过程中仅允许一次 `draft→published` 落定（同时写 `html_ref/status/render_status`），发布后仅 `pdf_ref/render_status/updated_at` 可更新。服务端仓储只暴露 `FinalizePublished` 与 `MarkRenderMetadata`，不提供发布后的内容更新方法；集成测试用 SQL 尝试更新已发布内容字段必须失败（应用层用例覆盖；DB 触发器可选，不作为 P4 必需）。

**`delivery_report_ref`**（append-only；`updated_at` 仅初始化）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_report_ref` |
| `report_id` | BIGINT | NOT NULL，FK `REFERENCES delivery_report(id) ON DELETE CASCADE` |
| `report_revision_id` | BIGINT | NOT NULL，FK `REFERENCES delivery_report_revision(id) ON DELETE CASCADE` |
| `kind` | TEXT | NOT NULL，CHECK `requirement/scenario/case_version/wave/case_run/issue/version_sut` |
| `ref_id` | BIGINT | NOT NULL（逻辑引用对应实体） |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`uk_delivery_report_ref_report_revision_id_kind_ref_id UNIQUE (report_revision_id, kind, ref_id)`；`idx_delivery_report_ref_kind_ref_id (kind, ref_id)`；`idx_delivery_report_ref_report_id_id (report_id, id)`。

### 3.4 `delivery_0003_analysis_run.sql`（order #23，delivery 上下文，1 表）

> D34 映射：HLD 字面 `analysis_run→delivery_analysis_run`。

**`delivery_analysis_run`**

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_delivery_analysis_run` |
| `version_id` | BIGINT | NOT NULL（逻辑引用 version） |
| `kind` | TEXT | NOT NULL，CHECK `failure/coverage/triage` |
| `status` | TEXT | NOT NULL DEFAULT 'queued'，CHECK `queued/running/done/failed/degraded` |
| `dedupe_key` | TEXT | NOT NULL，CHECK `char_length BETWEEN 1 AND 200` |
| `input_ref` | JSONB | NOT NULL DEFAULT '{}' |
| `output` | JSONB | NOT NULL DEFAULT '{}' |
| `agent_run_id` | BIGINT | NULL（逻辑引用 agent_run；1:1） |
| `error` | TEXT | NOT NULL DEFAULT ''，CHECK `char_length <= 2000` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`partial unique uk_delivery_analysis_run_version_id_kind_dedupe_key (version_id, kind, dedupe_key) WHERE status IN ('queued','running')`；`uk_delivery_analysis_run_agent_run_id UNIQUE (agent_run_id) WHERE agent_run_id IS NOT NULL`；`idx_delivery_analysis_run_version_id_kind_id (version_id, kind, id DESC)`。
P4 写入方仅 `issue.triage`；`failure/coverage` 仅保留枚举（P3 已把结果写 `case_run/step/assertion` 与事件，不重复写），P7 如需回填另立契约。

### 3.5 `version_0005_requirement_closure.sql`（order #24，version 上下文，1 表）

**`requirement_closure`**

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_requirement_closure` |
| `requirement_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES requirement(id) ON DELETE CASCADE` |
| `requirement_version_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES requirement_version(id) ON DELETE CASCADE` |
| `close_round` | INTEGER | NOT NULL，CHECK `> 0` |
| `check_result` | JSONB | NOT NULL（服务端重算的校验快照；HLD `checks` 的 DB 字段映射） |
| `evidence_refs` | JSONB | NOT NULL DEFAULT '[]'（issue/case_run/report 引用） |
| `closed_by_run_id` | BIGINT | NULL（逻辑引用 agent_run） |
| `closed_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `reopen_count` | INTEGER | NOT NULL DEFAULT 0，CHECK `>= 0` |
| `created_at` / `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`uk_requirement_closure_requirement_id_close_round UNIQUE (requirement_id, close_round)`；`idx_requirement_closure_requirement_id_id (requirement_id, id DESC)`；`idx_requirement_closure_requirement_version_id (requirement_version_id)`。
重开时更新最新 closure 的 `reopen_count + 1`；再次关闭插入 `close_round = 已有最大轮次 + 1`。closure 行不删除。

### 3.6 既有表使用（不 ALTER）

- `version.closed_at/close_reason/close_run_id`（P0 已有）：P4 写入/清空。
- `run_wave.report_id`（P3 已有）：P4 回填 wave report id。
- `content_ref`（P0）：报告 HTML 的 owner/role 约定见 §6。
- `human_request`（P3）：待办入口只读。
- `requirement.status`（P1）：P4 新增 `closed` 写入与 `closed→draft` 重开路径；`obsolete` 只读。
- `work_item`（P1）：新增 5 个任务类型；不改表结构。
- 不新建 chain/todo 表（HLD L229）。

### 3.7 命名与结构规范自检（Agent.md §6.9/D34）

| 检查项 | P4 结论 | 落点 |
|---|---|---|
| 表名 | delivery 上下文 7 表统一 `delivery_*` 前缀（`delivery_issue`、`delivery_issue_ref`、`delivery_issue_status_history`、`delivery_report`、`delivery_report_revision`、`delivery_report_ref`、`delivery_analysis_run`）；`requirement_closure` 归 version 家族；全部小写单数、无 `tbl_`/`tb_` 前缀、非保留字 | §3.2–§3.5 |
| 主键 | 全部 `id BIGINT` 雪花（应用生成），约束名 `pk_<表名>` | §3.2–§3.5 |
| 唯一/索引 | `uk_<表名>_<列...>`、`idx_<表名>_<列...>`；partial unique 名称保留列、谓词写在 `WHERE`；**无未命名 UNIQUE** | §3.2–§3.5 |
| 审计列 | 8 表均有 `created_at/updated_at`；append-only 表的 `updated_at` 仅插入初始化（比 P3 不可变例外更严格） | §3.1 |
| 外键 | 同上下文显式 FK（delivery_issue_ref/delivery_issue_status_history/delivery_report_revision/delivery_report_ref/requirement_closure、delivery_issue.merged_into_id）；跨上下文仅 BIGINT 逻辑引用（version/run_wave/agent_run/content_ref/identity） | §3.2–§3.5 |
| 字段 | lower_snake；本期无布尔字段（无需 `is_`）；无保留字；JSON/API 中 64-bit ID 十进制字符串 | §3.1 |
| 迁移 | `<context>_<NNNN>_<desc>.sql`；#21–#24 只追加；不改 #1–#20 | §3.2–§3.5、§14 |
| 字段映射 | HLD `issue/report/report_revision/report_ref/analysis_run`→DB 加 `delivery_` 前缀；HLD `checks`→DB `check_result`；`html_ref/pdf_ref`→`content_ref.id` | §3.3、§3.5、§6 |
| 既有代码 | P0–P3 的已冻结迁移/历史命名问题不在 P4 修改（§6.3 禁止改已应用迁移）；P4 代码不得复制违规模式；审计结论进 handoff backlog | §18 |

> 说明：D34 表名规则是“推荐 `业务名称_表的作用`”。P4 delivery 上下文统一 `delivery_*` 前缀（与 P2 `case_*`、P3 `execution_intent` 同思路），避免 `issue`/`report` 这类泛名；HLD/API/事件仍用 `Issue*`/`Report*` 语义名，DB 仅做映射。若用户要求改回 HLD 字面名，需同步改 DS §10/迁移/代码，属契约变更，P4 不自行改。

---

## 4. 契约：API / 事件 / 任务 / 工具 / 设置

### 4.1 通用约定

- 全部路径前缀 `/api`；鉴权复用 P0（登录用户）。版本读/写为登录用户；版本内问题/报告操作同版本写权限；不做新的 RBAC。
- ID 在 JSON/URL/工具参数中一律十进制字符串；时间 RFC3339（UTC）。
- 列表复用 P0 envelope `{data, next_cursor, has_more}`，参数 `before`/`limit`（默认 20，最大 100）；链路为单对象 + 预算截断。
- 错误复用 P0 信封与 reason：`invalid_argument`、`not_found`、`forbidden`、`state_conflict`、`lease_lost`；新增业务冲突一律用 `state_conflict` 并在 `details.reason` 给稳定子码（如 `issue_not_open`、`merge_cycle`、`report_not_current`、`close_predicate_failed`）。
- 幂等：问题创建/去重靠 `UNIQUE(version_id,dedupe_key)`；状态变更靠状态机 + 可选 `expected_revision`；报告生成靠 `report` 行锁 + `UNIQUE(report_id,revision)`；版本 close 靠条件 UPDATE；不引入全局 `Idempotency-Key` 中间件（P0/P1 现状）。
- 所有写操作写审计（`audit_log`）；报告 HTML 下载不记录内容到日志。

### 4.2 API 清单（OpenAPI `0.2.0-p4`，新增 14 ops）

**问题**

| 方法/路径 | 请求 | 响应 | 说明 |
|---|---|---|---|
| `GET /api/versions/{versionId}/issues` | query `status?`、`category?`、`severity?`、`version_sut_id?`、`before?`、`limit?` | `IssueListEnvelope` | 版本内问题列表；默认按 id DESC |
| `GET /api/versions/{versionId}/issues/{issueId}` | — | `IssueDetailView` | 含 refs/history/triage 摘要 |
| `POST /api/versions/{versionId}/issues/{issueId}/confirm` | `{reason?, expected_revision?}` | `IssueView` | `open→confirmed`；否则 409 |
| `POST /api/versions/{versionId}/issues/{issueId}/dismiss` | `{reason?, expected_revision?}` | `IssueView` | `open→dismissed`；否则 409 |
| `POST /api/versions/{versionId}/issues/{issueId}/reopen` | `{reason(1..2000), expected_revision?}` | `IssueView` | `confirmed/dismissed→open`；`reopen_count++`；否则 409 |
| `POST /api/versions/{versionId}/issues/{issueId}/merge` | `{target_issue_id, reason?, expected_revision?}` | `IssueView` | 源 `open/confirmed/dismissed→merged`；目标必须同版本、非 merged、非自身、非成环；否则 409 `merge_cycle/merge_target_invalid` |

`IssueView` 字段：`id, version_id, title, summary, status, category, severity, source, expected_revision, merged_into_id?, merged_at?, reopen_count, refs[], triage?, created_at, updated_at`；`IssueDetailView` 追加 `history[]`（`from_status,to_status,reason,actor_id?,actor_kind,created_at`）与 `related_refs[]`（按 kind 分组计数）。
`refs[]` 元素：`{kind, ref_id, version_sut_id?, role}`；`triage`：`{analysis_run_id?, status?, classification?, duplicate_of_issue_id?, summary?}`（只读投影，无独立 API）。

**报告**

| 方法/路径 | 请求 | 响应 | 说明 |
|---|---|---|---|
| `GET /api/versions/{versionId}/reports` | query `kind?`、`before?`、`limit?` | `ReportListEnvelope` | 版本总结默认在前，wave 报告按 wave id DESC |
| `GET /api/versions/{versionId}/reports/{reportId}` | — | `ReportDetailView` | 含 `revisions[]`、`refs[]`、`current_revision` |
| `GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}` | — | `ReportRevisionView` | 单 revision 元数据 |
| `GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}/html` | — | `text/html` 字节 | 鉴权 + 版本归属校验；`ETag`=sha256；`Cache-Control: private, no-store`；`X-Content-Type-Options: nosniff`；CSP 见 §6.4 |
| `POST /api/versions/{versionId}/reports/{reportId}/regenerate` | `{reason?}` | `202 {report_id, revision, work_item_id?}` / `409 state_conflict` | 幂等：同一 `revision` 生成中返回 202；仅 report 不存在/状态不允许时 409；新 revision append-only |

`ReportView`：`{id, version_id, wave_id?, kind, status, input_fingerprint?, current_revision?, created_at, updated_at}`。
`ReportRevisionView`：`{id, report_id, revision, status, as_of, input_fingerprint, template_version, render_status, html_available, pdf_available:false, created_at}`。
`ReportDetailView`：`ReportView + revisions[] + refs[]`；`refs[]` 元素 `{kind, ref_id}`。
`current_revision` 为 `ReportRevisionView` 摘要；`html_available = revision.status='published' AND render_status='done' AND html_ref IS NOT NULL`。

**链路**

| 方法/路径 | 请求 | 响应 |
|---|---|---|
| `GET /api/versions/{versionId}/exploration-chain` | `sut_id?`(=version_sut_id)、`page_id?`、`wave_id?`、`status?`、`result?`、`event_from?`、`event_to?`、`focus_node?`、`limit?` | `ChainView` |

`ChainView`：

```json
{
  "version_id": "…",
  "generated_at": "…",
  "snapshot_cursor": 123,
  "filters": {"sut_id": null, "page_id": null, "wave_id": null, "status": null, "result": null, "event_from": null, "event_to": null},
  "counts": {"requirements": 2, "scenarios": 3, "cases": 5, "waves": 4, "case_runs": 9, "issues": 2, "reports": 5, "evidence": 12},
  "nodes": [{"id": "req:1", "kind": "requirement", "ref_id": "1", "label": "…", "status": "clarified", "version_sut_id": null, "requirement_id": "1", "created_at": "…"}],
  "edges": [{"from": "req:1", "to": "scn:2", "kind": "has_scenario"}],
  "truncated": false,
  "truncation": {"max_nodes": 300, "max_edges": 600, "max_requirements": 100, "reason": ""},
  "replay": {"mode": "live", "wave_id": null, "event_from": null, "event_to": null, "approximate": true}
}
```

节点 `kind`：`requirement/scenario/case_version/wave/case_run/issue/report/closure`；边 `kind`：`has_scenario/has_case/executed_by/contains/raised/reports/closed_by/included_in`。步骤/断言/证据不建节点，进详情 Sheet（由 `case_run` 节点关联计数与深链）。

**版本与需求**

| 方法/路径 | 请求 | 响应 | 说明 |
|---|---|---|---|
| `GET /api/versions/{versionId}` | — | `VersionView` 扩展 | 增 `closed_at?/close_reason?`（`status` 已有三态） |
| `POST /api/versions/{versionId}/reopen` | `{reason(1..2000)}` | `VersionView` | 仅 `completed→running`；清 `closed_at/close_reason/close_run_id`；发事件；否则 409 |
| `GET /api/versions/{versionId}/requirements` | — | 既有 `RequirementListEnvelope` 扩展 | `RequirementView` 增 `closed_at?/closure?` |
| `POST /api/versions/{versionId}/requirements/{requirementId}/reopen` | `{reason(1..2000)}` | `RequirementView` | 仅 `closed→draft`；closure.reopen_count++；触发版本重开；否则 409 |

`RequirementView` 扩展：`status` 已存在；新增 `closed_at?`、`closure?: {closure_id, close_round, closed_at, reopen_count, checks?}`。`checks` 只在详情返回摘要，不返回内部 SQL/凭据。

### 4.3 事件（events.md §13，10 个）

| type | 频道 | payload 关键字段 | 说明 |
|---|---|---|---|
| `issue.created` | `version:{id}` | `issue_id, status, category, severity, wave_id?, case_run_id?` | 候选创建 |
| `issue.updated` | `version:{id}` | `issue_id, from_status, to_status, reason?` | 确认/驳回 |
| `issue.reopened` | `version:{id}` | `issue_id, reopen_count, reason` | 重开 |
| `issue.merged` | `version:{id}` | `issue_id, merged_into_id, reason?` | 合并 |
| `report.published` | `version:{id}` | `report_id, report_revision_id, kind, wave_id?, revision, as_of, html_ref` | HTML 发布 |
| `report.stale` | `version:{id}` | `report_id, kind, current_revision, reason` | 输入变化 |
| `requirement.closed` | `version:{id}` | `requirement_id, requirement_version_id, closure_id, close_round, closed_by_run_id?` | 闭环 |
| `requirement.reopened` | `version:{id}` | `requirement_id, from_status, to_status, reason` | 重开 |
| `version.closed` | `version:{id}` + `system` | `version_id, close_run_id?, report_revision_id, closed_at, close_reason` | 收尾 |
| `version.reopened` | `version:{id}` + `system` | `version_id, trigger, reason?` | 重开；`trigger=requirement_created/requirement_reopened/case_confirmed/execution_intent/manual` |

- 事件只作 UI 投影，发布失败不回滚业务；reconciler/DB 为真值。payload 不含 HTML、证据字节、凭据、密码。
- 复用 P0 envelope/seq/Last-Event-ID/stream.reset；版本状态事件同时发 `system` 频道（P0 既有约定）。

### 4.4 任务、Profile、工具（task-types.md §6；agent-profiles-tools.md §7）

| task type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `issue.triage` | delivery | `issue_triager` | `triage:{issue_id}` | `{issue_id,version_id}` | `analysis_run`（done/degraded）+ 团队帖 + 合并/分诊建议（不改 issue 状态） | 可重试；最终 dead 不影响 issue/报告 |
| `report.wave` | delivery | `report_writer` | `report:{report_id}:r{revision}` | `{report_id,wave_id,revision}` | 新 `report_revision`（HTML published）+ 事件 | 可重试；失败时 report/revision 保持 draft/failed，reconciler 补；不伪造 |
| `report.version` | delivery | `report_writer` | `report:{report_id}:r{revision}` | `{report_id,version_id,revision}` | 同上 | 同上；版本收尾被阻断直到 published |
| `requirement.close` | steward | `version_steward` | `req-close:{requirement_id}:{requirement_version_id}:{close_round}` | `{requirement_id,requirement_version_id,close_round}` | `requirement_closure` + `closed` + 事件 | 校验失败 409（可重试）；最终 dead 可见；版本收尾阻断 |
| `version.close` | steward | `version_steward` | `ver-close:{version_id}:{report_revision_id}:{predicate_fingerprint}` | `{version_id,report_revision_id,predicate_fingerprint}` | `completed` + `closed_*` + 事件 | 事务复核失败 409；最终 dead 可见；不自动降级 |

- 创建者：reconciler 检测到 wave 终态且无 published wave report → 创建 report 行 + ensure `report.wave`；版本报告条件满足（§7.4）→ ensure `report.version`；issue 候选创建后 ensure `issue.triage`；需求闭环条件满足 → ensure `requirement.close`；版本谓词满足 → ensure `version.close`。API `regenerate` 计算 `revision = max+1` 后 ensure 新任务。
- `report.render_pdf` 继续保留、不注册（P7）。
- 任务租约/心跳/退避/`dead` 沿用 P1/P3；`requirement.close`/`version.close` 不写 `waiting_human`。

**Profiles**

| key | 名称 | 预算 | 允许工具 |
|---|---|---|---|
| `issue_triager` | 问题分诊 | `{max_turns:6,soft_seconds:180,hard_seconds:300}` | `context.task_pack`、`context.team_room_read`、`issue.read`、`issue.propose` |
| `report_writer` | 报告撰写 | `{max_turns:6,soft_seconds:180,hard_seconds:300}` | `context.task_pack`、`context.team_room_read`、`report.data_read`、`report.narrative_propose` |
| `version_steward` | 版本管家 | `{max_turns:6,soft_seconds:180,hard_seconds:300}` | `context.task_pack`、`context.team_room_read`、`requirement.read`、`version.close_read`、`requirement.goal_met`、`version.close_propose` |

**工具**（scope 由运行时注入；参数不含 `versionId`；所有写操作服务端校验）

| 工具 | 参数 | 语义 |
|---|---|---|
| `issue.read` | `{issue_id?}` | 读 issue/refs/关联 case_run/step/assertion/evidence 摘要；无 issue_id 时读当前任务 issue |
| `issue.propose` | `{classification?, duplicate_of_issue_id?, merge_reason?(<=500), confidence?}` | 只写 `analysis_run.output` + 团队帖；禁止改状态；`duplicate_of_issue_id` 必须同版本、非 merged、非自身 |
| `report.data_read` | `{}` | 读当前任务 wave/version 的确定性统计快照（不含证据字节/凭据） |
| `report.narrative_propose` | `{summary_markdown(1..4000), highlights?[]}` | 只提交叙述候选；服务端 HTML 转义后写入本次 revision；不改变统计数字 |
| `requirement.read` | `{requirement_id?}` | 读需求版本/用例/执行/issue 摘要 |
| `version.close_read` | `{}` | 返回 close 谓词逐项快照与未满足原因 |
| `requirement.goal_met` | `{requirement_id?, note?}` | 服务端重算闭环校验；通过写 closure+closed+事件；失败 409 带失败项 |
| `version.close_propose` | `{reason(1..500)}` | 服务端事务内重算谓词；通过则 completed；失败 409 带失败项 |

- 提示词约束（写入 profile system prompt，代码常量）：只调用白名单工具；不得伪造统计/证据；不得尝试确认需求/用例、合并/重开问题、关闭版本（除 `version.close_propose`）；失败时按工具返回的缺失项说明，不循环重试无效调用。
- 工具注册：`delivery/application/tools.go`（issue/report）、`version/application/tools.go`（goal_met/close_propose）；由 `cmd/eztest` 注册进 agent registry；单测校验工具名/schema/scope 注入。

### 4.5 设置（settings.md §4d）

| key | 类型 | 默认 | 说明 |
|---|---|---|---|
| `chain.max_nodes` | int | 300 | 链路节点上限（1..1000） |
| `chain.max_edges` | int | 600 | 链路边上限（1..2000） |
| `chain.max_requirements` | int | 100 | 参与链路的版本需求上限（1..500） |
| `chain.replay_event_limit` | int | 500 | 历史回看事件上限（1..2000） |

- 均非 secret、热加载；越界回退默认并审计。
- 既有 `report.*`：`template_version` 固定 `"1"`；`section_summary/results/issues/coverage/evidence` 在 P4 HTML 渲染中生效；`report.render_pdf=true` 保留但 P4 不消费（P7）；`paths.report_dir` 标记“P4 不使用”（与 `paths.export_dir`/`paths.evidence_dir` 例外一致）。
- `.env` 仍严格 4 项，不新增 env。

### 4.6 导出与 `export_artifact`

- P4 不扩展 `export_artifact`；报告 HTML 通过 §4.2 的 html 端点直接下载（鉴权、sha、版本归属校验）。
- `export_artifact` 的 report/chain owner_kind 与 html/pdf format 保留给：链路导出 → **P6**；报告 PDF 导出 → **P7**。在 handoff 移交表显式登记，避免继续孤儿化。

### 4.7 OpenAPI 与冻结

- `info.version: 0.2.0-p4`；根 `x-frozen: P4`；新增 tags `Delivery`/`Chain`；新增 14 ops、约 14 schemas。
- P0/P1/P2/P3 既有 paths/schemas 语义不变；RequirementView/VersionView 只增可选字段。
- 阶段末把 `openapi.yaml`、`db-schema.md` §10、`events.md` §13、`task-types.md` §6、`agent-profiles-tools.md` §7、`settings.md` §4d、`migrations-order.md` §6、`contracts/README.md` 标 `frozen: P4`。
- `scripts/p2-contract-check.cjs`/`p3-contract-check.cjs` 改为“P2/P3 增量仍在 + 不硬编码总数”；新增 `scripts/p4-contract-check.cjs`（冻结模式）断言：P0–P3 路径/schema/事件/表/设置仍在；P4 增量齐全；`x-frozen: P4`；迁移 24；设置/事件/任务/profile 的 P4 清单逐项存在（以契约 §14 实际清单为准，不写死旧阶段总数）。
- `migrate_test.go`/`migrate_integration_test.go` 由“等于 20”改为“>= 20 且包含 P4 四文件”，并断言 P4 表存在。

---

## 5. 问题（issue）设计

### 5.1 候选生成（确定性）

- 入口一：消费 `execution.failure_candidate` 事件（低延迟）；入口二：reconciler 每轮扫描 `case_run.status='failed' AND failure_class='application'` 且无 `issue_ref(kind='case_run')` 的 case_run（兜底漏事件/重启）。
- 只处理 `failure_class='application'`；`interrupted/skipped/blocked` 等不建 issue。
- **dedupe_key**：优先使用 P3 payload `dedupe_hint`（必须匹配 `^[0-9a-f]{32}$`）；缺失或不合法时按 P3 同一算法重算：`sha256(fmt.Sprintf("%d|%d|%d|%d|%s", versionID, caseVersionID, stepSeq, assertionSeq, failureClass))[:32]`。`stepSeq/assertionSeq` 取该 case_run 中失败断言的最小 `(step_seq, assertion_seq)`；无断言时均取 0。该键跨波次稳定（不包含 wave_id/case_run_id）。
- **标题/摘要（确定性合成，不依赖 LLM）**：
  - 标题：`"{case_title} · step {stepSeq}: {category} failure"`；无 step 时为 `"{case_title} · {category} failure"`；截断 500。
  - 摘要：优先 `case_run.summary`；否则失败断言的 `expected/actual/reason` 拼接；截断 4000。
  - `category`：`db/api/ui`（P3 派生规则）；`severity`：`major/minor/trivial`（P3 派生规则）。
  - `fingerprint = sha256(canonical JSON{version_id,case_run_id,case_version_id,step_seq,assertion_seq,dedupe_key,title,category,severity})`（64 hex）。
- **refs（上限 20）**：`case_run`（必有）、`case_version`、`wave`、首个失败 `step_result`、失败 `assertion_result`（≤3）、`evidence`（≤5，仅 id 引用）、可解析的 `requirement`。
- 写入：`INSERT ... ON CONFLICT (version_id,dedupe_key) DO NOTHING`；随后补 refs（`ON CONFLICT DO NOTHING`）；首次创建写 `issue_status_history(from=NULL,to='open',actor_kind='system')`；发 `issue.created`；audit；ensure `issue.triage`。
- 幂等：重复事件/扫描不新增 issue/refs/history；`created_by_run_id=NULL`（确定性创建）。

### 5.2 状态机与操作

| 当前 | 动作 | 目标 | 约束 |
|---|---|---|---|
| `open` | confirm | `confirmed` | 任意登录用户（版本写权限） |
| `open` | dismiss | `dismissed` | 同上；reason 可选 |
| `confirmed` | reopen | `open` | reason 必填（1..2000）；`reopen_count++` |
| `dismissed` | reopen | `open` | 同上 |
| `open/confirmed/dismissed` | merge(source→target) | `merged` | 目标同 version、非 merged、非自身、非成环；reason 可选 |
| 其他 | 任意 | — | 409 `state_conflict`（`issue_not_open`/`issue_not_reopenable`/`merge_target_invalid`/`merge_cycle`） |

- 每次状态变更写 `issue_status_history`（from/to/reason/actor_id/actor_kind=user）、`expected_revision++`、audit、事件；`merged` 写 `merged_into_id/merged_at/merged_by`。
- 合并：`merged_into` 链最多跟随 16 跳；目标到达源即 `merge_cycle`；源 issue 的 refs 不迁移（P7 做 alias/迁移）；读侧 UI 对 `merged` 显示“已合并到 #id”并给主问题深链。报告/链路在生成时把源 issue 归到主 issue 计数并保留 `source_issue_ids`（仅计数，不展开）。
- `merged` 在 P4 为终态；不提供 unmerge/reopen。
- 权限：与版本写一致（登录用户）；AI 不得调用这些 API/工具（工具白名单不含）。

### 5.3 分诊（analysis_run）

- `issue.triage` 任务由候选创建后 ensure；profile `issue_triager` 只读 issue/关联执行，调用 `issue.propose` 提交 `{classification?, duplicate_of_issue_id?, merge_reason?, confidence?}`。
- 服务端校验：`duplicate_of_issue_id` 同版本、存在、非 merged、非自身；`classification` ∈ 允许分类（沿用 category + `duplicate/expected_failure/flaky` 建议值，存 output JSON 不落 issue 字段）；`merge_reason` ≤500；非法返回 400 `invalid_argument`。
- 写入：`analysis_run(kind='triage', status='done', input_ref={issue_id,refs}, output=...)`；团队帖 finding；**不改 issue 状态**。无建议时 status=done、output 为空摘要。
- 失败：analysis_run status=degraded/failed，error 记录；任务按 P1 重试上限；最终 dead 在团队频道/待办暴露；issue/报告不受阻。
- API 详情 `triage` 投影取最新 analysis_run（done/degraded）。

### 5.4 测试

- 单测：dedupe_key 稳定（同 case_version 跨 2 wave 相同、case_version 变化不同）；标题/摘要合成；状态机非法迁移；合并自环/环/跨版本；reopen_count/history；merged 读侧归并。
- 集成：候选事件→issue+refs+history+事件；重复事件幂等；reconciler 兜底；API 6 个端点 + CAS 409；合并链；analysis_run 1:1；权限；审计。
- 前端：列表/筛选/详情/动作/合并弹窗/深链。

---

## 6. 报告（report）设计

### 6.1 输入与指纹

- **wave 报告**输入：`run_wave`（id/status/trigger/counts/by_version_sut/finished_at）、该 wave 的 `case_run`（id/case_version_id/status/failure_class/summary）、step/assertion 计数与失败项、evidence 计数与 `(id,kind,sha256,size)`、关联 issues（id/title/status/severity/category/source_issue_ids）、version_suts（名称标签）。
- **版本报告**输入：version、requirements（id/title/status/closure）、waves（id/status/counts/report_id）、case_run 汇总、issues（含 merged 归并）、coverage（P3 覆盖计数/缺口）、version_suts、wave 差异（通过/失败/blocked 计数）。
- **canonical JSON**：结构体字段固定顺序；数组按 id ASC（issue 表按 id DESC 仅展示）；不使用 map 作为指纹输入；时间戳只用业务真值（as_of 不参与指纹）。
- `input_fingerprint = sha256(canonical JSON)`；`as_of = 生成开始时刻 UTC`；`template_version="1"`。
- 规模上限：单报告 issues ≤500、waves ≤200、case_runs ≤2000；超出按 id 取前 N 并在报告中明确“截断说明”；HTML ≤2 MiB。
- 空数据合法（空 wave/无 issue），报告仍生成。

### 6.2 确定性 HTML 渲染

- Go `html/template`，固定内置模板（不可用户编辑），无外部资源；内联 CSS；不使用 `template.HTML` 注入用户文本。
- 章节按 `report.section_summary/results/issues/coverage/evidence` 开关（false 时不渲染）；`report.template_version` 固定 `"1"`。
- 叙述：`report.narrative_propose` 的 `summary_markdown` 按**纯文本**转义渲染（保留换行），不允许原始 HTML/Markdown 链接/脚本；作为 summary 章节的“AI 叙述（候选）”，统计数字仍来自确定性数据。
- 安全：所有用户可控文本自动转义；HTML 上限 2 MiB；下载端点 CSP：`default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'`；`X-Content-Type-Options: nosniff`。
- 若渲染后超过 2 MiB：按 issues/waves/case_runs 顺序逐步减半并写入“报告截断说明”；不得静默丢弃；仍超限则任务失败（可见）。

### 6.3 内容账本与发布

- 写入顺序（同一任务内）：
  1. 计算指纹/as_of，创建 `report_revision(status='draft', render_status='pending', revision=next)`（`next = COALESCE(MAX(revision),0)+1`；行锁 report + 唯一键防并发）。
  2. 渲染 HTML；`storage` 写 `content_object`（sha256/size/mime=`text/html; charset=utf-8`、相对路径）并登记 `content_ref(owner_kind='report_revision', owner_id=revision.id, role='report_html')`。
  3. 更新 revision：`html_ref=content_ref.id, render_status='done', status='published'`；更新 report：`current_revision_id=revision.id, status='published', input_fingerprint`；写 `report_ref`；回填 `run_wave.report_id`（wave）；audit；发 `report.published`。
- 任一步失败：revision 置 `render_status='failed'`、保持 draft（或删除该 revision？**不删除**，留审计）；任务失败重试时创建下一 revision。旧 published revision 不受影响。
- 旧 revision 保持 `published`（内容/as-of 不可变）；`report.current_revision_id` 指向最新 published。
- `pdf_ref` P4 恒 NULL，`pdf_available=false`；`report.render_pdf` 不调度。

### 6.4 下载与鉴权

- `GET .../html`：校验版本归属（report.version_id = path versionId）、revision 属于 report、`status='published' AND render_status='done'`；按 `html_ref → content_ref.id` 加载，校验 `owner_kind='report_revision' AND owner_id=revision.id AND role='report_html'`；读 `content_object` 字节，校验 sha256/size；返回 `200 text/html`，`ETag: "<sha256>"`、`Cache-Control: private, no-store`、CSP、nosniff；不暴露文件路径、object_id 以外内部字段。
- 404：revision 不存在/未发布/内容缺失；409：内容 sha/size 校验失败（`state_conflict` + `report_content_corrupt`）。

### 6.5 stale 与重生成

- 触发标记：`issue.*`（created/updated/reopened/merged）、`requirement.closed/reopened`、新 case 确认、wave 终态/新 wave report、version 重开。交付侧事件消费者在 `report.status='published'` 时置 `stale` 并发 `report.stale`（每个 current revision 只发一次；已 stale 不重复）；wave 报告与版本报告都适用。
- 收尾谓词重算指纹，若 `report.input_fingerprint != ComputeVersionFingerprint()` → 视为 stale（并补标/补事件），close 谓词不满足。
- `POST regenerate`：计算 `next_revision`，ensure `report.wave|version` 任务（dedupe `report:{report_id}:r{revision}`）；同一 next revision 的活跃任务存在时幂等返回 202；不删除旧 revision；生成期间旧 current 仍可读。
- 版本报告 ensure 条件：无 draft/生成中的版本报告；需求全 closed/obsolete；无活跃 intent/wave；触发后生成；若最新版本报告指纹不匹配则再生成。

### 6.6 测试

- 单测：指纹确定性（同输入同 hash；issue 状态变→hash 变）；章节开关；HTML 转义（`<script>`/`"`/`&`）；空数据；超限截断；narrative 纯文本；revision 编号。
- 集成：repo 唯一/append-only 约束；content_ref owner/role；下载鉴权/sha 校验/CSP 头；发布事务；旧 revision 不变；stale 事件仅一次；regenerate 新 revision；wave.report_id 回填；report_ref。
- 前端：revision 切换/as-of/stale/HTML 打开/重生成。

---

## 7. 需求闭环与版本收尾/重开

### 7.1 需求状态与闭环写入

- 新增写路径：`clarified→closed`（`requirement.goal_met` 服务端校验后）；`closed→draft`（用户重开 API 或新 draft 版本创建）。
- `obsolete` 仍只读（P6 写路径）。
- `requirement_closure`：每次通过写一行（`close_round = 已有最大轮次+1`）；`check_result` 保存服务端重算快照；`evidence_refs` 存 `case_run_id/issue_id/report_id` 引用；`closed_by_run_id` = agent_run id。
- 重开：最新 closure `reopen_count++`（运营元数据，可更新）；requirement.status 置 `draft`；发 `requirement.reopened`；触发版本重开；需求当前版本不变（P1 语义），下一次澄清/确认流程沿用既有路径。
- 新 draft 版本创建（P1 既有写入路径）：若 requirement.status=`closed`，在同一服务调用链中置 `draft`、closure.reopen_count++、发事件、版本重开。

### 7.2 闭环校验（服务端唯一真值）

`requirement.goal_met` 只提交意图；服务端重算以下 checks（全部通过才写 closed）：

| check | 通过条件 |
|---|---|
| `requirement_state` | status ∈ {clarified, closed} 且 `current_version_id` 非空 |
| `has_confirmed_case` | 该 requirement 当前 current+confirmed case_version 数 ≥1 |
| `cases_terminal` | 每个 current+confirmed case_version 的最新 case_run（id DESC）状态 ∈ {passed,failed,blocked,inconclusive,skipped}（不得 pending/running） |
| `failed_case_has_issue` | 每个最新 `failed(application)` case_run 至少有一条 `issue_ref(kind='case_run')`（issue 状态不限；open 不阻断） |
| `no_open_work` | 除本 requirement 的 `requirement.close` 任务自身外，无 open/claimed/running 作用域 work_item（含 design.gap/design.requirement 等） |
| `no_active_intent` | 该 requirement 的 current+confirmed case 无 open/claimed/running execution_intent |
| `no_inflight_case_run` | 该 requirement 的 case 无 pending/running case_run |
| `coverage_recorded` | 该 requirement 的 current+confirmed case_version 均出现在覆盖统计中（可读取；缺失视为不通过） |

- 通过：insert closure；update requirement.status='closed', closed_at, closed_by_run_id；audit；事件 `requirement.closed`；任务 done。
- 不通过：工具返回 409 `close_predicate_failed`，`details.failed` 列出 check 名与计数；不改状态；agent 可解释后结束；任务按重试上限；reconciler 在状态变化后以新 close_round 重试。
- 失败/未修问题不阻断闭环；**但失败应用用例必须有 issue 引用**；P4 无 waived UI（check JSON 预留 `waived=false`，P6 再做人工豁免）。

### 7.3 版本 close 谓词（唯一口径）

`version.close_propose` 与 reconciler 使用同一服务函数；全部满足：

1. `version.status='running'`。
2. 无 work_item 状态 ∈ {open,claimed,running,waiting_human}，**排除本版本的 `version.close` 任务自身**；`report.*` 等任务仍计入。
3. 无 `execution_intent` 状态 ∈ {open,claimed,running}。
4. 无 `human_request` 状态 `open`。
5. 无 `run_wave` 状态 ∈ {planned,running,paused}。
6. 无 `case_run` 状态 ∈ {pending,running}。
7. 该版本所有 requirement.status ∈ {closed,obsolete}。
8. 存在 `kind='version'` 报告：`report.status='published'`、current revision `status='published' AND render_status='done' AND html_ref IS NOT NULL`、`report.input_fingerprint == ComputeVersionFingerprint()`（当前输入重算）。
9. 无 `report.version` 生成中任务（由 check 2 覆盖）。

- `predicate_fingerprint = sha256(canonical(version_id, requirement 状态集合, report_revision_id, open_counts...))`，用于 close 任务 dedupe；状态变化自然产生新 key。
- 满足 → reconciler ensure `version.close`；不满足 → 不创建任务；已存在任务在谓词失效时由服务端复核拒绝（409）。

### 7.4 版本报告与收尾顺序

1. requirement 全部 closed/obsolete + 无 intent/wave → reconciler ensure `report.version`（若最新版本报告缺失或 stale）。
2. `report.version` published 且指纹当前 → ensure `version.close`。
3. Steward `version.close_propose` → 服务端事务内重算 → 条件 UPDATE `WHERE id=? AND status='running'` → 写 `closed_at/close_reason/close_run_id` → audit → `version.closed`。
4. 关闭后不删除任何数据；报告/问题保持可读。

### 7.5 重开触发与实现

| 触发 | 实现点 | 事件 trigger |
|---|---|---|
| 新需求创建 | version 需求创建服务（P1 路径）调用 `ReopenIfCompleted` | `requirement_created` |
| 需求重开（API 或新 draft 版本） | requirement 服务 + `POST .../requirements/{id}/reopen` | `requirement_reopened` |
| 新用例确认 | design 确认服务（P2 路径）调用 `ReopenIfCompleted` | `case_confirmed` |
| 新 execution intent | P3 既有 `version.Reopen` 路径 | `execution_intent` |
| 人工重开 | `POST /api/versions/{id}/reopen` | `manual` |

- `ReopenIfCompleted`：条件 UPDATE `completed→running`，清 `closed_at/close_reason/close_run_id`；若最新版本报告存在则置 stale（发 `report.stale`）；发 `version.reopened`；audit。非 completed 时 no-op。
- 并发：close 与 reopen/新 intent 用条件 UPDATE + 行锁；测试至少覆盖“close 时并发新 intent”“completed 后人工 reopen”“reopen 后再次 close”。

### 7.6 测试

- 单测：8 个 checks 的正/负样例；失败应用用例无 issue 阻断；open issue 不阻断；`version.close` 自排除；谓词 9 条逐条；指纹变化；reopen 清字段。
- 集成：closure 唯一轮次；并发 close（一个 200 一个 409）；close×intent 竞态；report.version 先于 close；完成后新需求自动 running；人工 reopen；事件/审计。
- Agent：Steward 工具校验（scope、参数、409 details）；fake runner 单测；真实 LLM 验收在 §12。

---

## 8. 探索链路（chain）读模型

### 8.1 数据来源（只读，不直读他域表）

| Provider | 来源（application 端口） | 提供 |
|---|---|---|
| `VersionChainProvider` | version application | requirements（id/title/status/closure）、version_suts、closed_at |
| `DesignChainProvider` | design application | scenario/case_version 当前与**历史**（按 id 任意状态读取，修 superseded 标题为空）、case→requirement、page/version_sut 目标 |
| `ExecutionChainProvider` | execution application | waves（id/status/counts/finished_at）、case_runs（id/wave/case_version/status/failure_class）、step/assertion 计数、evidence 计数/kind/sha |
| `DeliveryChainProvider` | delivery application | issues（含 merged_into/refs）、reports（kind/status/current revision/as_of） |
| `ResourceChainProvider` | resource application | version_sut→SUT/环境/页面名称（label/filter） |

- `chain` 不写任何表；不订阅写操作；不缓存持久化。请求级组装。
- 历史 reader：design application 新增 `ReadCaseVersionsByIDs(ctx, ids)`（不限制 current/confirmed，返回 title/revision/status/superseded 信息），仅 chain 使用；不得改变 P3 CaseExecutor 的 current+confirmed 读取语义。

### 8.2 节点/边与计数

- 节点 `kind`：requirement、scenario、case_version、wave、case_run、issue、report、closure。
  - requirement：status/closure 摘要；scenario：name；case_version：title/revision/status/result（最新 case_run 聚合）；wave：status/counts；case_run：status/result/failure_class/evidence_count/step_count/assertion_count；issue：status/severity/category/merged_into；report：kind/status/revision/as_of；closure：closed_at/reopen_count。
- 边：`has_scenario`、`has_case`、`executed_by`、`contains`、`raised`、`reports`、`closed_by`、`included_in`、`merged_into`（当目标 issue 也在图中）。
- 步骤/断言/证据不建节点；`case_run` 节点带计数与详情入口；详情 Sheet 通过既有 execution/details 端点按需加载（不把大证据带进链路响应）。
- 节点 `id` 稳定字符串：`{kind}:{ref_id}`；`ref_id` 一律字符串。

### 8.3 过滤、预算与截断

- 过滤：`sut_id`（=version_sut_id）、`page_id`、`wave_id`、`status`、`result`；作用到节点及诱导边；`counts` 为过滤后计数；`focus_node` 强制包含目标节点（即使被过滤），其祖先补最短路径。
- 预算：`chain.max_requirements` 限制根需求（按 updated_at DESC, id DESC），`chain.max_nodes`/`chain.max_edges` 限制总图。
- 截断顺序（超出时从低优先级丢弃）：先丢 issue→case_run→report→wave→case_version→scenario，最后才丢 requirement；closure 随 requirement 保留；已在 `focus_node` 的节点及其祖先永不丢。`truncated=true`、`truncation.reason="node_budget|edge_budget|requirement_budget"`；UI 必须显示提示。
- 空版本返回空 nodes/edges + counts 0，不 404。

### 8.4 实时、历史回看与深链

- 实时：前端复用 `sse-client` 订阅 `version:{id}`，收到 P4 事件与既有 wave/case_run/evidence/requirement 事件后 300–500ms 去抖重取 `exploration-chain`；保留“跟随执行”开关与连接状态。
- 历史回看（P4 两档，不承诺任意 as-of）：
  1. **SSE 补播**：`Last-Event-ID`/`after` 断线补齐事件流（P0 既有），用于恢复到当前。
  2. **按波次/事件区间**：`wave_id` 过滤只读图；`event_from/event_to` 由服务端通过 eventing 将 seq 映射到时间边界，过滤实体 `created_at <= 边界`，`replay.approximate=true`；UI 显示“历史回看/近似”标识，不写“as-of 快照”。事件时间线通过 `GET /api/events?channel=version:{id}&after=…`（P0 既有）读取，最多 `chain.replay_event_limit` 条。
- 深链：`?node=`、`?issue=`、`?report=`、`?wave=`；选中节点写 URL，刷新可恢复；`focus_node` 保证深链目标不被截断。
- `snapshot_cursor` 返回当前频道最新 seq；前端以它作为重连基线。

### 8.5 测试

- 单测：过滤/诱导边/focus 祖先/预算截断顺序/稳定 id/空版本/merged 归并边。
- 集成：5 个 provider 组装；历史 CaseReader 返回 superseded 标题；事件 seq→时间边界；跨 SUT 用例节点；报告/closure 边；`truncated` 与 settings 覆盖。
- 前端：图渲染/过滤/图例/详情/实时/波次回看/深链/截断提示/空错载。

---

## 9. 前端设计

### 9.1 版本详情与头部

- 现有 8 Tab 骨架不变；`chain|issues|reports` 三个占位替换为真实 Panel。
- VersionHeader：状态徽标（`initial/running/completed`）、`closed_at/close_reason`（completed 时 tooltip/文本）、“重新打开”（completed 时，确认弹窗，reason 必填）、“查看探索链路”、待办入口（open human_request 数 + 待设计 CTA）。
- Tab 计数：问题 `open` 数、报告最新 revision/状态；计数来自对应列表/概览查询，缺失时显示空而不是 0 误导。
- 失败态：close/reopen/报告下载错误统一 toast + 内联重试。

### 9.2 问题 Tab（`features/delivery`）

- 布局：顶部筛选（status/category/severity/SUT）+ 列表（Table）+ 右侧详情 SideSheet（宽度 ≥360，URL `?issue=`）。
- 列表字段：标题、状态、类别/严重度、SUT、refs 计数、reopen_count、更新时间；默认 `open` 优先（`status` 排序：open→confirmed→dismissed→merged，再 id DESC）。
- 详情：摘要、refs（case_run/wave/evidence 深链）、状态历史、triage 摘要（classification/duplicate 建议/confidence）、动作按钮（确认/驳回/重开/合并）；`merged` 显示主问题链接。
- 合并弹窗：搜索同版本非 merged 问题（输入 ID 或标题过滤）；确认后刷新列表与详情；成环/非法错误内联。
- 重开弹窗：reason 必填；成功后状态/计数/历史刷新。
- 空/加载/错误态；无 code anchor（P5 占位不显示）。

### 9.3 报告 Tab（`features/delivery`）

- 左列表：默认“版本总结”，可切波次报告（wave id/状态/as_of）；状态徽标 published/stale/draft+render_status。
- 右详情：当前 revision、as-of、template_version、input_fingerprint（短）、章节开关结果；“打开报告”（api-client 拉 HTML→Blob→新窗口/下载，失败提示）；“重新生成”（stale/手动，确认后 202 并轮询/事件刷新）；revision 下拉可看旧 revision（只读）；PDF 显示“P7”禁用。
- stale 徽标与“输入已变化，建议重新生成”提示；生成中显示进度/禁用重复按钮；失败显示 error/reason。

### 9.4 链路 Tab（`features/chain`）

- 画布：复用 React Flow（已有依赖）与 design layout 经验；四列/分层布局（需求→场景/用例→波次/Run→报告/闭环/问题），节点/边语义色使用 `globals.css` token（不抄 mock hex）。
- 控件：SUT/页面/波次/状态/结果过滤；实时跟随开关；波次历史选择 + 事件时间线播放（近似标识）；图例；统计栏（需求/用例/波次/证据/问题/覆盖）；小地图；截断 Alert。
- 详情 Sheet：节点元数据 + 关联深链（issue/report/wave/case_run/evidence）；步骤/断言/证据计数与入口；merged issue 显示主问题。
- 深链：`?node/issue/report/wave` 与 URL 同步；复制深链按钮。
- 全宽布局（覆盖 `app-content` max-width 限制仅本 Tab）；空/加载/错误/截断态；超大图用预算+提示，不静默丢节点。

### 9.5 待办入口（Topbar TodoDrawer）

- Topbar 新增待办按钮（当前版本上下文）：打开 Drawer 列出 `GET /api/versions/{id}/human-requests?status=open` 的条目（kind/摘要/过期时间）+ 待设计 CTA（复用 PendingDesignCTA）；点击跳转执行 Tab/接管或设计 Tab。
- 只读；不新增 `/api/todos`；无当前版本时显示空态；P6 再做全局聚合。

### 9.6 共享组件与规范

- 新增 `SutChip`/`PageChip`/`StatusDot`/`FilterChip`/`SideSheet` 到 `src/components/ui`；已存在的 Tabs/Table/Dialog/Alert/EmptyState/ErrorState/LoadingCard 复用。
- 颜色/间距/字体使用语义 token；报告/链路不引入 mock 的固定 1600×1100 画布与 9px 字号；响应式 ≥1280 全宽，<1024 可滚动。
- 请求只走 `api-client`，SSE 只走 `sse-client`；URL 为深链真值；不新增全局 store 依赖。

### 9.7 测试

- Vitest：Issue 列表/筛选/动作/合并/重开；Report 列表/revision/stale/HTML 打开失败；Chain 过滤/截断/深链/图例/空错载；TodoDrawer；VersionHeader 状态/重开。
- E2E（§12）：真实 LLM 浏览器流程 + 截图。

---

## 10. 事件/SSE 集成

- 发布：业务事务提交后 best-effort 发布；失败只记录 audit/日志，不回滚；reconciler 兜底状态。事件写入 P0 `event` 表，seq 连续。
- 订阅：`version:{id}` 频道；前端 `useVersionEvents` 扩展映射：
  - `issue.*` → 刷新 issues 列表/详情/计数；
  - `report.*` → 刷新 reports/详情/当前 revision；
  - `requirement.closed/reopened`、`version.*` → 刷新版本头/报告/链路；
  - 既有 wave/case_run/evidence/human_request → 链路/执行视图。
- 去抖 300–500ms；断线重连按 Last-Event-ID；`stream.reset` 时全量快照重取。
- 历史回看使用 `GET /api/events`（P0 既有）分页读取事件，最多 `chain.replay_event_limit`；不新建事件表/频道。
- 事件 payload 不包含报告 HTML、证据字节、凭据；UI 只做投影，不以事件重建业务真值。

---

## 11. 配置、迁移与启动

- `.env` 仍严格 4 项（D49）；P4 不新增 env。
- 新增 4 个 `chain.*` 设置（§4.5），其余复用 `report.*`；`paths.report_dir`/`report.render_pdf` 在 settings 文档注明 P4 不使用/不消费。
- 启动顺序沿用 P0：env → DB → 迁移（24）→ seed settings/profiles → server/worker/reconciler。P4 在 `cmd/eztest` 注册 delivery/chain 服务、agent profiles、reconciler 规则、工具。
- 迁移只追加；dev 库迁移前备份；验收库从零迁移 24 并验证二次执行 no-op。
- 单实例假设：P4 的 reconciler/report/close 与 P3 一致；多副本安全留 P7（D67）。

---

## 12. 演示与验收（真实 LLM）

### 12.1 验收原则

- **只使用真实 LLM**：`scripts/p4-acceptance.ps1` 与 `scripts/p4-e2e.ps1` 读取 `.env` 小写 `llm_*`，经 admin API 写入 `llm_config`，不启用 fake LLM 上游；验收证据记录真实模型名/耗时/turn 数。
- 真实 LLM 失败按**系统缺陷**处理：上下文包缺失/错误、工具 scope/schema 不可用、提示词歧义、服务端校验过严、超时/预算错误。修复后重跑；不得以 fake 替代或把行为失败标为“模型不稳定”后跳过。
- 仅外部网络/供应商短时中断可记录 blocked 并重试；重试上限与证据写入 handoff。
- 单测/集成中的 fake port 不属验收，不在 §12 证据表出现。

### 12.2 环境与数据准备

- 隔离库 `eztest_v02_p4`；API `14344`、Web `16869`、executor `14346`；复用 P3 fixture（订单/支付双变体，端口 `13335/13337`）与 fixture DB（`p3_orders`/`p3_refunds`）。
- 启动顺序：迁移 24 → API/executor/fixture → admin 登录 → 真实 LLM 配置 → 资源（2 SUT + 环境 + 页面 + 目标库 + 账号）→ 版本绑定 2 SUT。
- 需求/用例准备沿用 P3 裁决方式：SQL 预置 draft 需求/用例 + P1 需求确认 API + P2 用例确认 API（P2 已验收设计流程，P4 不重复跑设计 LLM）。
- 执行：确认用例自动 intent/波次 → 真实 Case Executor 执行 → 至少 1 个 `failed(application)`（断言失败）产生 `execution.failure_candidate`。
- 数据规模：主版本 2 需求、3 用例（1 失败、1 通过、1 跨 SUT/blocked 视 fixture）、≥2 波次；另起第二个小版本用于“待设计阻断收尾”。

### 12.3 验收场景（脚本断言 + 证据）

| # | 场景 | 关键断言 | 证据 |
|---|---|---|---|
| S1 | 问题候选/去重 | 失败 case 自动建 issue（`open`）、refs 含 case_run/case_version/wave；同一 case_version 第二波再失败仍 1 条 issue；确认→confirmed、驳回→dismissed、重开→open（reopen_count=1/history）、合并→merged+merged_into；非法合并 409 | API 响应/DB 查询/日志 |
| S2 | wave 报告自动发布 | wave 终态后自动生成 HTML 报告；`report/report_revision` published；`html_ref` 指向 content_ref；下载字节 sha/size 一致；`run_wave.report_id` 回填；`report_ref` 有 wave/case_run/issue | API/DB/sha 对比 |
| S3 | revision/as-of/stale | 修改 issue 后 version report 变 stale（事件一次）；`POST regenerate` 生成 revision 2；revision 1 HTML 字节不变；as_of 递增；旧 revision 仍 published | API/DB/文件 sha |
| S4 | 需求闭环 | 真实 Version Steward 调 `requirement.goal_met`；两条需求 closed + closure 行（check_result 完整）；失败应用用例有 issue 引用；open issue 不阻断；无 issue 的失败用例（构造）校验 409 | DB/API/事件/LLM 日志 |
| S5 | 版本报告与收尾 | 版本总结 published 且指纹当前；Steward `version.close_propose` 成功；version completed、closed_at/close_reason/close_run_id 写入；`version.closed` 事件；close 任务自排除验证 | API/DB/事件 |
| S6 | 重开 | 新需求创建 + 人工 `POST reopen` 均使 completed→running、清 closed_*、发事件；版本报告 stale；再次收尾可回 completed | API/DB/事件 |
| S7 | 链路 | 实时 SSE 更新；SUT/页面/波次/状态/结果过滤；`?node/issue/report/wave` 深链；按波次历史回看 ≥2 波；superseded 用例标题非空；旧 report revision 可读；issue 历史可读；调低 `chain.max_nodes` 后 `truncated=true` 且 UI 提示 | API/截图 |
| S8 | human_request 待办 | P3 2FA 接管产生 open human_request → Topbar Drawer 可见并可跳转；resolve 后消失 | API/截图 |
| S9 | 待设计阻断 | 第二版本存在 clarified 未设计需求 → 无 closed/无 report.version/不 close；确认用例并执行后可闭环收尾 | DB/API |
| S10 | 浏览器 E2E | 版本 Tab、问题动作、报告打开/revision/stale、链路过滤/回看/深链、收尾状态/重开、待办 Drawer；截图 | Playwright 日志+截图 |

### 12.4 浏览器 E2E 与截图

- `scripts/p4-e2e.ps1` 启动 Web（真实 LLM），Playwright（Chrome channel）执行 S10；截图存 `.tmp/p4-acceptance/screenshots/`；
- 覆盖 1280/1600 宽度；链路 Tab 全宽；无横向溢出；控制台无 error（允许既有 SSE 重连日志）。

---

## 13. 测试策略与门禁

### 13.1 命令与期望

| 证据 | 命令 | 期望 |
|---|---|---|
| 全量门禁 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` | EXIT=0；Go 包全 ok；frontend 全 PASS；日志 `.tmp/p4/final-gate.log` |
| 后端单测 | `go test -count=1 ./...`（`.tools/go`） | 0 FAIL；新增 delivery/chain/version domain 覆盖 ≥80% |
| 集成/迁移 | `go test -tags=integration ./internal/db/... ./internal/delivery/... ./internal/chain/... ./internal/version/...` | 迁移 24 二次 no-op；8 张 P4 表；仓储/服务/路由通过 |
| 前端单测 | `pnpm test` | 既有 + P4 新增用例全部 PASS |
| 类型/构建 | `pnpm typecheck`、`pnpm lint`、`pnpm build` | EXIT=0 |
| P4 契约 | `node scripts/p4-contract-check.cjs` | P0–P3 增量仍在；P4 paths/schemas/events/tables/tasks/profiles/settings 齐全；`x-frozen: P4` |
| P2/P3 契约兼容 | `node scripts/p2-contract-check.cjs`、`node scripts/p3-contract-check.cjs` | PASS（增量仍在，不硬编码总数） |
| P1 回归 | `scripts/p1-acceptance-llm.ps1`（真实 DeepSeek） | PASS；真实 LLM，不用 fake 替代 |
| P2 回归 | `scripts/p2-acceptance-llm.ps1`（真实 DeepSeek）+ `scripts/p2-e2e-llm.ps1`（浏览器） | PASS；真实 LLM，不用 fake 替代 |
| P3 回归 | `scripts/p3-acceptance.ps1`（真实 DeepSeek，16 场景）+ `scripts/p3-e2e.ps1` | PASS；P4 新增 version/design 钩子，必须回归执行语义；不用 fake 替代 |
| P4 API 验收 | `powershell.exe -File scripts/p4-acceptance.ps1` | S1–S9 PASS（真实 LLM）；日志/scenarios `.tmp/p4-acceptance/` |
| P4 浏览器 E2E | `powershell.exe -File scripts/p4-e2e.ps1` | S10 PASS；截图 `.tmp/p4-acceptance/screenshots/` |

### 13.2 覆盖重点（phase plan §5 Review Focus 映射）

1. **契约漂移**：p4-contract-check + 冻结 diff + §14 表格；P0–P3 语义不变。
2. **多 SUT 身份混用**：报告 `by_version_sut`、链路 `version_sut_id` 过滤、跨系统用例、issue refs 的 version_sut_id 测试。
3. **上下文压缩丢事实**：Steward/Report/Issue Triager 的任务包断言（task pack 含 wave/requirement/report 真值，不依赖会话回放）；长输入截断有提示。
4. **执行幂等/收尾**：close 条件 UPDATE、close×intent/新需求竞态、report regenerate 并发、重复事件去重、迁移幂等。
5. **旧假设回流**：issue 四态（无 reopened 枚举）、version 三态、单活跃波、report append-only；回归测试断言不出现 0.1 的整版冻结/8 状态机。

### 13.3 证据规范

- 每个场景写 `.tmp/p4-acceptance/scenarios.txt`：编号、命令、耗时、关键断言、结果；
- handoff §3 表列命令、结果、路径；不得只写“已通过”；
- 截图至少：版本头 completed、问题 Tab 详情、报告 revision/stale、链路实时+历史+截断、待办 Drawer。

---

## 14. 契约变更与冻结

| 文件 | P4 变更 | 冻结 |
|---|---|---|
| `contracts/db-schema.md` | §10：`delivery_issue`/`delivery_issue_ref`/`delivery_issue_status_history`/`delivery_report`/`delivery_report_revision`/`delivery_report_ref`/`delivery_analysis_run`/`requirement_closure` 逐列/约束/索引/不变量；§5 上下文归属增 `delivery`（7 表）；`chain` 无表说明；既有表使用说明 | `frozen: P4` |
| `contracts/migrations-order.md` | §6：`delivery_0001_issue_lifecycle`(#21)、`delivery_0002_report_revision`(#22)、`delivery_0003_analysis_run`(#23)、`version_0005_requirement_closure`(#24)；ownership | `frozen: P4` |
| `contracts/events.md` | §13：10 个 P4 事件/payload/频道 | `frozen: P4` |
| `contracts/openapi.yaml` | `0.2.0-p4`；14 ops；tags Delivery/Chain；新增 schemas；Version/Requirement 视图可选字段 | `x-frozen: P4` |
| `contracts/task-types.md` | §6：5 个任务类型；`report.render_pdf` 仍 §4 保留 | `frozen: P4` |
| `contracts/agent-profiles-tools.md` | §7：3 profiles、8 工具、scope 注入、校验 | `frozen: P4` |
| `contracts/settings.md` | §4d：4 个 chain 键；report_dir/render_pdf 注记 | `frozen: P4` |
| `contracts/README.md` | P4 冻结状态/证据 | — |
| `contracts/executor-protocol.md` | 不变 | 沿用 P3 |
| `scripts/p2|p3-contract-check.cjs` | 前向兼容（增量仍在，不硬编码 20/49/20） | 测试调整，非契约 |
| `scripts/p4-contract-check.cjs` | 新增冻结检查 | 新 |
| `migrate_test.go`/`migrate_integration_test.go` | “>=20 且含 P4” | 测试调整 |

- 破坏性变更：无。P0–P3 表/字段/迁移不改；Version/Requirement 仅加可选字段；既有事件/任务语义不变。
- 冻结时点：真实 LLM 验收通过后；冻结后 P5+ 只能消费，需变更先停止并更新 contracts/裁决/handoff。

---

## 15. 并行执行映射

| 流 | 写作用域 | 依赖 | 验收入口 |
|---|---|---|---|
| Lead | contracts、migrations、`cmd/eztest`、`server`、agent seed/registry、contract-check、handoff | 无 | 契约检查/编译/集成 |
| W1 issue | `delivery/{domain,application,infrastructure,interfaces/httpapi}/issue*`、`analysis*` | Lead 契约/迁移/types | issue 单测/集成/API |
| W2 report | `delivery/**/report*`、`render*`、`content*` | Lead 契约/迁移/types | report 单测/集成/HTML |
| W3 closure+Steward | `version/**/closure*`、`version/**/close*`、`version/**/tools*`（只改 version 上下文；agent seed/registry 由 Lead 收口） | Lead 端口/types | closure/close 单测/并发 |
| W4 chain | `chain/**` | Lead 契约/端口/历史 reader | chain 单测/集成 |
| W5 frontend | `frontend/src/features/{delivery,chain}`、`versions`/`shell` 增量、`components/ui` 新组件 | Lead OpenAPI/既有 execution 模式 | vitest/typecheck/E2E |
| W6 acceptance | `scripts/p4-*`、`frontend/e2e/p4.spec.ts`、fixture 扩展、`.tmp/p4-acceptance/*` | Lead 契约/脚本骨架 | 验收脚本/E2E |

- 依赖图：`Lead 契约+迁移+types` → W1/W2/W3/W4/W5/W6 并行 → Lead 集成（路由/seed/装配）→ `scripts/test.ps1` → P4 验收 → 契约冻结 → handoff。
- W3 通过 Lead 预定义端口（`IssueReader`/`ReportReader`/`ExecutionCoverageReader`）解耦 W1/W2；W5 只依赖 OpenAPI schema，不等待后端完成；W6 可先写脚本骨架与断言，待 API 就绪运行。
- 共享文件冲突：contracts/migrations/cmd/server/seed 禁止子代理并发写；W1/W2 共享 `delivery/domain/types.go` 由 Lead 先落；出现 FS_STALE_VERSION 时重读 rebase。
- 每流 TDD、频繁小提交；Lead 每流合并前跑该流测试 + `go build ./...`。

---

## 16. P4-D 裁决表（含设计偏差与批准）

| 编号 | 裁决 | 依据/说明 | 用户批准 |
|---|---|---|---|
| P4-D-01 | **P4 纳入最小需求闭环**：`requirement_closure` + `requirement.close` + `goal_met` + 服务端校验 + 关闭/重开 | HLD L155/L272/L470；ARCH L238/L249；P1S L197/361；P3S L396；补 P4 In 遗漏 | 2026-11-01 用户选择“纳入 P4 最小需求闭环” |
| P4-D-02 | **报告 HTML 以 content ledger 为权威**：`html_ref=content_ref.id`；自动发布；旧 revision 保持 published/as-of；固定内置 HTML + 现有章节开关；不写 `paths.report_dir`；`report_ref` 建表；PDF 留 P7 | ARCH L542/HLD L624；HLD L203/L611；P2-D-29/P3-D-20 先例；用户逐项选择 A | 2026-11-01 用户选择 A |
| P4-D-03 | **P4 做基础重开**：`confirmed/dismissed→open`（reason/history/reopen_count）+ 基础合并；merged 重开/跨波次去重/AI 合并/refs 迁移留 P7 | D32 L82；phase-04 L19/L28/L60；用户逐项选择 A | 2026-11-01 用户选择 A |
| P4-D-04 | **两档回放**：SSE 实时补播 + 按波次/事件区间历史回看；不承诺任意时点 as-of；300/600/100 预算 + `truncated`；补历史 CaseReader | D47 L118；ARCH L429；HLD L229；phase-04 L46/L54；用户逐项选择 A | 2026-11-01 用户选择 A |
| P4-D-05 | **analysis_run 三 kind 枚举、P4 仅 `issue.triage` 写入**；status `queued/running/done/failed/degraded`；failure/coverage 不回声（P3 已落 case_run/事件） | HLD L201；P3S L9/L60/L869；P3H L119；设计收窄 | 2026-11-01 用户认可设计 v1 |
| P4-D-06 | **Version Steward + 服务端复核**：profile/任务/工具；事务内重算谓词；`waiting_human` 以 open human_request/paused wave 等价；AI 不直改状态 | ARCH L338/L421；HLD L281/L487；P3H L87；D06/D40 | 2026-11-01 用户认可设计 v1 |
| P4-D-07 | **补重开触发**：新需求/需求重开/新确认/新 intent（P3）/人工 API；completed 只做重开扫描 | HLD L484/L307-308；P3H L112；phase-04 L22 | 2026-11-01 用户认可设计 v1 |
| P4-D-08 | **版本内待办入口**（Topbar TodoDrawer 读 open human_request + 待设计 CTA）；全局 `/api/todos`/待办中心留 P6；不建 todo 表 | phase-04 L23；P3H L113；HLD L529/L564；phase-06 L242；P3-D-16 | 2026-11-01 用户认可设计 v1 |
| P4-D-09 | **闭环校验**：失败应用用例须有 issue 引用（open 不阻断）；无 waived UI；Agent 提议 + 服务端重算；self-task 排除 | HLD L470；ARCH L249；D07“失败/未修不阻断” | 2026-11-01 用户认可设计 v1 |
| P4-D-10 | **契约先行 + 门禁前向兼容**：先契约/迁移再实现；p2/p3 检查改增量仍在；新增 p4 检查 | contracts README L4；P2 先例；phase plan §4 | 2026-11-01 用户认可设计 v1 |
| P4-D-11 | **验收只用真实 LLM**；真实 LLM 失败=系统缺陷信号（上下文/提示词/工具/scope），按根因修复重跑；fake 仅单测/集成端口 stub，不属验收；P1/P2/P3 回归同样使用各自真实 LLM 验收脚本 | 用户 2026-11-01 纠正；P3S L66；P2-D-30 的双轨仅保留单测/集成端口 stub，不用作 P4 验收 | 2026-11-01 用户明确纠正 |
| P4-D-12 | **分支** `feat/v0.2-p4` 从 `feat/v0.2` 拉出，完成后合回；保留阶段分支 | P3H §0 惯例；用户“在 v0.2 上工作” | 2026-11-01 用户认可设计 v1 |
| P4-D-13 | **导出归属**：P4 报告 HTML 直下，不扩 `export_artifact`；报告 PDF 导出→P7、链路导出→P6；handoff 显式登记 | HLD L205/L520；P2H L151；避免孤儿化 | 2026-11-01 用户认可设计 v1 |
| P4-D-14 | **P4 固定内置 HTML + 现有 `report.section_*` 开关**；不做模板编辑器/模板版本化/PDF/worker（P7）；对 phase-04 Out“模板”一词做窄化解释 | ARCH §12.1；HLD L611；OD-2；phase-04 L27 | 2026-11-01 用户选择报告 A（含章节开关） |
| P4-D-15 | **chain 不建表**（只读 provider 聚合）；phase-04 L33“链路表”为基线措辞偏差，记录不改设计文档 | HLD L229；ARCH §8.8；phase-04 L59；用户 2026-11-01 要求核实出处后认可设计 v1 | 2026-11-01 用户认可设计 v1 |
| P4-D-16 | **历史 DB 约束名修复（33 内联 PK + 8 未命名 UNIQUE）不在 P4 执行**，作为独立合规任务记入 handoff/P8 backlog；布尔字段不改名；P4 新表按 D34 严格命名 | Agent.md §6.3（已应用迁移不可改）、§6.9.2（既有字段名不改）；用户 2026-11-01 选择“只修 P4 新表，历史记 backlog” | 2026-11-01 用户选择 |

- 任何实现中发现新偏差：先在 spec §16 追加 `P4-D-16+` 并在回复中报告，不得静默改设计。

---

## 17. 风险、边界与回滚

1. **真实 LLM 质量风险**：按用户原则，真实 LLM 失败视为系统缺陷（上下文/提示词/工具/scope/校验），不得归因“模型不稳定”或用 fake 绕过；验收前先做单场景探针（Issue Triager/Report Writer/Steward 各 1 次），探针失败先修系统再跑全量。
2. **close 谓词自排除/竞态**：`version.close` 任务自身必须排除；close 与 new intent/需求/用例确认并发用条件 UPDATE + 事务重查；必须有并发测试。
3. **报告指纹/stale 一致性**：canonical JSON 固定顺序；输入变化→stale；close 前重算指纹；避免事件丢失导致 stale 漏标。
4. **HTML 安全/大小**：全量转义、纯文本 narrative、CSP、2 MiB 上限、截断提示；防 XSS/拒绝服务。
5. **content ledger 一致性**：report_revision 与 content_ref 双向 owner/role 校验；下载 sha/size 校验；孤儿文件 P7 GC。
6. **链路性能/预算**：节点/边/需求预算 + 截断提示；provider 查询限制；不直读他域表；历史 reader 只读。
7. **历史回放语义**：P4 只承诺两档；UI 不显示“as-of”；任意时点 as-of 留 P6/P7，不得在验收中宣称。
8. **开发库迁移**：dev 库仅 8 迁移，先备份再迁移；验收从零建库；不做 down migration，回滚=回退代码 + 保留 additive 表。
9. **契约门禁**：p3 检查硬编码会红，先改兼容模式；p4 检查需覆盖 P0–P3 仍在；migrate 测试改 >=。
10. **P2 设计对话永不关闭**：P4 用 requirement 状态/design_scope 实现“待设计阻断”，不修改 P2 对话模型，也不新增“关闭对话”能力。
11. **范围蔓延**：P5–P7 项只进 handoff/backlog；报告 PDF、全局中心、任意 as-of、问题增强不得提前实现。
12. **工作树污染**：用户专用文件/既有 dirty 文件不提交；禁止 `git add -A`；contracts/migrations 只由 Lead 写。

---

## 18. Handoff 要求（`phase-04-handoff.md`）

必须包含：

1. **完成范围**：逐项勾选 §1.2；列出 W1–W6 实际交付。
2. **契约变更**：§14 全表；冻结状态 `P4`；破坏性变更（应无）；P5+ 可依赖的路径/表/事件/任务/工具/设置。
3. **测试与验收证据**：§13 命令、结果、日志/截图路径；真实 LLM 场景矩阵；浏览器 E2E；回归结论。
4. **已知缺口/偏差**：P4-D 偏差表（尤其 chain 无表、PDF Out、issue 增强 Out、global 中心 Out）；未修复问题；是否影响下一阶段。
5. **未交付实体移交表**：`report.render_pdf/pdf_ref`→P7；`export_artifact` report/chain→P6/P7；全局问题/报告/待办/搜索→P6；chain v2/as-of→P6/P7；问题重开合并增强/跨波次去重/alias→P7；`requirement.obsolete` 写路径→P6；知识/GC/多实例→P7；代码/视觉→P5。每项写 owner、输入契约、验收建议。
6. **下一阶段输入**：P5 读 `report_revision/html_ref`、`issue_ref(kind=…)`、chain 节点模型、analysis_run output；P6 读全局中心/todo/导出/chain v2 契约；P7 读 `report_revision.pdf_ref/render_status`、content_ref 账本、issue `merged_into/reopen_count`、close 谓词。
7. **前置检查**：P5 启动前核对 P4 contracts `frozen: P4`、24 迁移、P4 验收证据、未交付实体表；缺 owner 即停。
8. **风险**：§17 裁剪为下一阶段关注项。
9. **裁决索引**：§16 + 用户 2026-11-01 裁决/纠正原话摘要（真实 LLM 原则）。
10. **规范自检与 backlog**：§2.5 的干净项/既有偏差（33 内联 PK、8 未命名 UNIQUE、7 无 `is_` 布尔列）；P4 新表合规证据；**`P4-D-16` 已裁决：历史约束名修复作为独立合规任务/backlog，不在 P4 执行；布尔字段不改名**。
11. **停止声明**：P4 完成即停止，不自动进入 P5；等待用户验收/批准。

---

## 19. 规格自审记录

- 占位符：无 TBD/TODO；所有 P4 实体有 owner/表/API/测试/验收口径。
- 一致性：chain 无表与 §8 一致；报告 ledger 与 §6 一致；issue/closure 状态机与 §5/§7 一致；契约计数以 §14 与实际检查脚本为准（本规格不写死总数）。
- 范围：只覆盖 P4；P5+ 在 §0.7/§18.5 显式移交。
- 歧义：`waiting_human`、`merged` 终态、`report.render_pdf`、`paths.report_dir`、`chain 任意 as-of` 均已给出唯一口径或显式 Out。
- 用户纠正已并入：§2.4、§12.1、§16 P4-D-11、§17.1。



