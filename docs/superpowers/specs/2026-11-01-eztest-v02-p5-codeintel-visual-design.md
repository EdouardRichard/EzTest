# EzTest 0.2 P5 设计（代码智能 + 视觉断言）— 实施规格

> 状态：实施规格（implementation spec）；2026-11-01 按用户逐项裁决（D-01…D-12）编写；待用户审阅后进入 `writing-plans`。
> 阶段：P5。阶段文件：`docs/version-V0.2/phases/phase-05-codeintel-visual.md`。
> 前置 handoff：`docs/version-V0.2/phases/phase-04-handoff.md`（P4 completed）。
> 本 spec 只实现 P5；P6/P7 内容只进 handoff/backlog，不实现、不提前改契约。
> 本 spec 不修改 `docs/version-V0.2/**`；与设计文档的偏差按 §16 `P5-D` 记录，并经用户批准。

---

## 0. 依据、阶段映射、前置检查与根因

### 0.1 权威来源与阅读依据

**权威顺序**：`docs/version-V0.2/architecture-design.md` → `high-level-design.md` → `research/decision-log.md` → `research/open-decisions.md` → `research/capability-matrix.md` → `phases/phase-05-codeintel-visual.md` → P0–P4 frozen `contracts/*` → P0–P4 handoff。

**本 spec 实际查阅（Lead + 只读子代理）**：

- `Agent.md`（全文；其 P1–P7 为 0.1 编号）。
- `docs/version-V0.2/README.md`；`architecture-design.md` §2、§5、§6、§7、§8、§9、§10、§12、§13–§17；`high-level-design.md` §1–§12（全文）。
- `research/decision-log.md`（全文）、`open-decisions.md`（全文）、`capability-matrix.md`（全文）。
- `implementation-phase-plan.md`（全文）；`phases/phase-05-codeintel-visual.md`、`phase-04-handoff.md`、`phase-03-handoff.md`、`phase-02-handoff.md`（边界）。
- `contracts/README.md`、`db-schema.md`、`events.md`、`agent-profiles-tools.md`、`task-types.md`、`settings.md`、`migrations-order.md`、`executor-protocol.md`、`handoff-template.md`、`openapi.yaml`（P5 相关路径/全部路径计数）。
- `ui/mock.html`、`ui/mock.css`、`ui/screenshot.cjs`（确认 mock 只覆盖需求/设计/链路三屏）。
- **0.1 设计参考（非 0.2 控制面权威）**：`docs/design/code-intelligence.md` §0–§11/§14；`docs/design/system-blueprint.md` §3.2/§5.4/§7/§19；`docs/design/detailed-design.md` §4.4–§4.6/§5.4–§5.5/§5.8/§8.3–§8.4/§9.3/§9.8/§10.3；`docs/design/frontend-design.md` §2/§4.3.8/§8/§9；0.1 P4 spec（代码智能）、0.1 P8 spec §2 D62/§4.2/§9.2–§9.3/§11；0.1 P5/P7 spec 的锚点/集成节。
- **0.1 验收/实现参考**：`docs/acceptance/p8/acceptance.md` §5–§6、`docs/acceptance/p8/e2e/README.md` 视觉路径；`docs/user-guide.md` §4.4/§7；`docs/operations.md` 视觉/GC；`scripts/fixture-codeintel-repo.ps1`、`scripts/acceptance-codeintel-llm.ps1`、`scripts/acceptance-p8-llm.ps1` 视觉段；`executor/src/visual.mjs`、`executor/src/actions.mjs`、`executor/src/contexts.mjs`；0.1 分支 `feat/p4-code-intelligence` 与 `.tmp/main-work`（只读 `git show`，不 checkout/不复制）。
- 现有 0.2 实现：`backend/internal/{agent,execution,design,version,resource,storage,eventing,audit,settings,platform,db,server}`、`frontend/src/features/*`、`executor/`、`scripts/test.ps1`、P1–P4 验收脚本。

**未读/不可用**：`docs/用户专用文件，禁止AI阅读.md`（遵守禁止项）；0.1 P4 的 `acceptance.md` 在仓库中不存在（只有 `.tmp` 结果与 P8 验收记录）；0.2 没有 P5 详细设计（见 §0.5）。

### 0.2 阶段映射与编号

- `implementation-phase-plan.md` §0：**P5 → `phase-05-codeintel-visual.md`；PREV_HANDOFF=`phase-04-handoff.md`；本阶段写 `phase-05-handoff.md`**。
- 实施分支：从 `feat/v0.2` 拉出 `feat/v0.2-p5`；完成后合回 `feat/v0.2`，保留 `feat/v0.2-p5` 作为阶段审计分支（P4 惯例）。
- 本 spec 的 `P5-D-xx` 只属于 P5，不复用 0.1 P0–P8 编号；0.1 阶段号在引用时写“0.1 Pn”。
- 0.1 的 P4=代码智能、P5=脑图、P8=视觉；0.2 的 P5=代码智能+视觉。凡出现两个编号体系，必须写前缀。

### 0.3 前置检查（实施开始前执行；不满足则停止报告）

1. `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → EXIT=0（P4 基线）。日志写 `.tmp/p5/baseline-test.log`。
2. `node scripts/p4-contract-check.cjs` → PASS；`node scripts/p3-contract-check.cjs`、`node scripts/p2-contract-check.cjs` → PASS（P4 基线）。
3. 迁移基线 = 24（`order.txt` 末项 `version_0005_requirement_closure.sql`）；P5 只追加 #25–#31。
4. `.env` 有真实 DeepSeek 凭据（小写 `llm_api_key/llm_base_url/llm_model_name`）；API 进程仍只读 4 项 `EZTEST_*` env；不得新增第 5 个启动 env。
5. executor `127.0.0.1:13336` 可启动（Playwright 1.64.x）；真实 Chrome 可用；P5 验收由脚本启动，不依赖手工进程。
6. PostgreSQL 17 @5433 可用；验收使用隔离库 `eztest_v02_p5`；开发库迁移前先备份。
7. 工作树 dirty 文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1`、用户专用文件）不属于 P5；禁止 `git add -A`；不得提交用户文件。
8. P4 handoff §5 的 P5 输入/前置检查已核对；P4 contracts `x-frozen: P4` 且 24 迁移。

### 0.4 用户裁决（2026-11-01，P5 会话）

| # | 裁决 | 说明 |
|---|---|---|
| D-01 | **根因只写 spec/handoff，不改 `docs/version-V0.2`** | 用户选择 A；P5 spec §0.5 记录根因与修正阅读清单；P5 handoff 记录 P6–P8 同类风险 |
| D-02 | **表模型 8 + 3，不增表** | 代码 8 表、视觉 3 表，严格按 HLD 名称；不建 `code_change_set`/`code_analysis_cache`/`code_fact_version`；变更集嵌入 `code_analysis.changed_files`，事实版本入 `code_fact` |
| D-03 | **走 work_item 任务板 + 专用 codeintel worker（B1a）** | 新增 `code` 团队；`index.repo`/`code.analyze` 为版本作用域 work item；按类型/团队领取；索引要求 `version_id` 归属；全局仓库后台索引留 P6/P7 |
| D-04 | **T0 确定性 Git + T1 AI 事实 + 降级（0.1 对齐）** | 不引入 parser；T1 仅变更集可读文本文件；服务端证据校验；限制进 DB 设置；LLM 不可用 → done+degraded |
| D-05 | **确定性 + AI 影响分析，含用例候选** | direct/contract/relation + 一次 AI 推理；subject 含 requirement/requirement_version/case_version/sut_page/sut_api；low 不自动选中 |
| D-06 | **统一 `code_anchor` + 追加式 `code_fact_review`** | 不改 `delivery_issue_ref` 枚举；`case_source(kind=code)` 保留并校验；锚点状态 valid/stale/broken |
| D-07 | **视觉基线执行后审批（A）** | 首次执行 actual + inconclusive；管理员从 actual 批准；retest 后 passed；新用例版本需重新批准；不自动复用 |
| D-08 | **executor 侧 `visual.compare` + visual 证据 kind（A）** | 协议 v1.2；pixelmatch；actual/diff 入内容账本；环境/配置指纹不一致判 inconclusive；四态 passed/failed/inconclusive/blocked |
| D-09 | **提议 A1 + 质量 Q1** | 代码事实 + 已有 network 证据生成 proposed；仅管理员 confirm/reject；最小 `/quality` 页 + 版本 visual overview |
| D-10 | **全量 UI + 只读/候选工具（A+T1）** | 代码 Tab、执行 visual 子视图、设计视觉配置、需求/问题锚点、资源提议、质量页；工具 code.* 只读 + `execution.visual_assert` + `sut.page_propose` |
| D-11 | **code_baseline 升级为 commit SHA（A）** | `version_sut.code_baseline_commit` 可空列；intent `code_baseline` 改为 commit 列表哈希；分析 base=该 commit，head=分支头；无仓回退 branch 哈希 |
| D-12 | **专用 fixture + 隔离环境（A）** | 新建 P5 Git/视觉 fixture；`eztest_v02_p5`；API 14345/Web 16870/executor 14347；真实 Chrome/DeepSeek；P1/P2 fake、P3/P4 真实回归；`feat/v0.2-p5` |

### 0.5 根因记录（为什么 P5 才发现没有详细设计）

- `9173c52`（2026-10-31 16:59）一次性写入 24 个 0.2 设计文件（含 9 个 phase 文件、+3902 行）；所有 phase 定义文件全历史仅此 1 次提交；`phase-05-codeintel-visual.md` 当前 blob 与基线完全一致（`2baa48e8…`），从未回写。
- 0.2 ARCH/HLD 是**蓝图级**：`high-level-design.md:5` 明确“本文不是 spec/plan/task；是进入 Superpowers 工程流前的‘设计蓝图’”。代码智能/视觉在 0.2 只有名称级（ARCH:352/623；HLD:31-32/226-227/341/522-523/564）。
- phase-05 的“输入”是**手工拼装**：ARCH §6.3/§7.3/§8.4–8.5/§17 在 0.2 中分别是设计与用例/工具/设计/执行/演进；决策 D20–D27 是 0.2 设计与用例决策、D39–D44 是 Agent runtime/上下文决策；`phase-05:15` 的“0.1 代码智能/视觉验收记录”没有文件路径。P6 phase 还引用了不存在的 D54–D58。
- 启动提示词（`implementation-phase-plan.md:19-46`）必读清单只含 0.2 文档 + phase + 上一 handoff + contracts + mock，**没有要求读 0.1 详细设计/P4 spec/P8 spec**；0.2 设计又没有承载 P5 机制，因此按提示词读全仍缺设计。
- phase 文件是在 0.2 设计评审（`.tmp/research/07-doc-rereview.md` 16:31）之后 16:43–16:46 写的，提交 16:59，未被评审；这与 P4 spec §0.5 记录的“批量写入、范围遗漏、引用漂移”同构。
- **修正方式（不改 0.2 设计文档）**：以 §0.1 的修正阅读清单为准；0.2 只取集成面，设计语义取 0.1 参考并按 0.2 架构/契约重写；缺失裁决进 §16 `P5-D`。P6–P8 同类风险写入 P5 handoff。

### 0.6 设计实体覆盖矩阵（P5 认领）

| HLD/ARCH 实体 | 设计出处 | Owner | P5 交付 |
|---|---|---|---|
| `code_index_run`/`code_entity`/`code_edge` | HLD §2.2:226 | **P5** | 表 + T0 索引 + API + worker |
| `code_fact`/`code_analysis`/`code_impact_candidate` | HLD §2.2:226 | **P5** | 表 + T1 事实/影响 + API + UI |
| `code_anchor`/`code_fact_review` | HLD §2.2:226；phase-05:19/46 | **P5** | 表 + 校验/复核 + API + UI |
| `visual_baseline`/`visual_assertion`/`visual_diff` | HLD §2.2:227 | **P5** | 表 + 配置/审批/比对/四态 + API + UI |
| `case_assertion.kind=visual` 执行 | HLD §2.2:174；P3-D-39 | **P5** | 移除 skipped 假通过；actual/diff/四态 |
| `code.*`/`sut.page_propose` 工具 | HLD §4.2:341；agent-profiles-tools §4b | **P5** | 9 + 1 个工具注册/白名单 |
| `execution.visual_assert` | phase-05:21；0.1 P8 | **P5** | 工具 + 执行集成 |
| 页面/API AI 提议 | HLD §6.1:426；phase-05:22 | **P5** | 生成 + proposed + 管理员确认 |
| 代码基线 commit | P3 spec:313；P3-D-09 | **P5** | `version_sut.code_baseline_commit` + intent 哈希升级 |
| 视觉质量指标 | HLD §8.1:564；phase-05:21/27 | **P5（占位）** | 版本 visual overview + 最小 `/quality` 页；完整质量中心 P6 |

### 0.7 不在 P5（显式 Out + owner）

- P6：完整质量中心/评测、全局搜索、待办中心、Agent 中心、chain v2、全局问题/报告中心完善。
- P7：报告 PDF/模板、跨版本知识、统一 GC、多实例编排、问题重开/合并增强。
- P5 不做 live HTTP probe BFS（页面/API 提议用代码事实 + 已有 network 证据）；留 P6。
- 视觉 mask/ignore region、动画/字体高级稳定策略、跨浏览器矩阵、AI 语义合并；留 P7/P8。
- 0.1 的 11 张代码表/1 张视觉表/文件系统证据/8 状态机/批次模型不移植。
- 不新建一级导航；不提前实现 `report.render_pdf`、`gc.run`、`knowledge.review`。

### 0.8 P5-D 裁决索引

完整表见 §16：`P5-D-01`…`P5-D-18`。实现与测试必须逐条可追溯；新增偏差先追加 `P5-D-19+` 并停止报告，不得静默改设计。

---

## 1. 目标与退出标准

### 1.1 目标

补齐 0.1 P0–P8 的差异化能力：代码索引/diff/影响/锚点/事实复核，以及视觉基线/审批/差异；把代码事实接入设计/执行 Agent，把视觉断言接入真实执行与证据；前端提供代码与变更、视觉子视图、锚点、提议、质量指标入口；契约冻结 P5。

### 1.2 退出标准（逐条可验证）

1. **索引/事实/影响**：本地 Git 仓索引完成（T0 文件树/diff），T1 产出 route/api/client_call/page/component/symbol 事实与证据；设计 Agent 通过 `code.*` 工具读到事实/路由/API；影响分析产出候选（含用例候选）并可勾选。
2. **锚点/复核**：问题/用例/需求可挂 `file:line` 锚点；校验返回 valid/stale/broken；重复 409；事实 correct/incorrect 复核追加式落库并有审计；incorrect 事实不进入自动影响。
3. **视觉**：视觉断言配置 → 首次执行 actual + inconclusive(missing baseline) → 管理员批准基线 → retest passed → 改页面 failed(application) → 环境/配置不一致 inconclusive；actual/diff 证据可下载且 sha/size 校验；四态分类正确；stale/非 current 用例不执行。
4. **提议/质量**：代码事实 + network 证据生成 proposed 页面/API；管理员 confirm/reject 生效；最小 `/quality` 页显示视觉指标。
5. **代码基线**：`version_sut.code_baseline_commit` 解析；intent `code_baseline` 为 commit 哈希；分析 base/head 可复现；无仓回退 branch 哈希。
6. **契约/迁移**：24 → 31 迁移；P5 `p5-contract-check.cjs` 通过；P2/P3/P4 契约检查前向兼容；`scripts/test.ps1` 全绿；P1–P4 回归通过。
7. **验收**：真实 DeepSeek + 真实 Chrome 的 P5 API/DB 验收 + 浏览器 E2E 通过；handoff 完成；contracts 冻结 `P5`。
8. **边界**：未实现 P6/P7 项；未修改 `docs/version-V0.2` 设计文档；未恢复 0.1 控制面假设。

### 1.3 演示路径

管理员配置 SUT/页面/API/代码仓 → 用户建版本并绑定 SUT/代码仓 → 澄清/确认需求 → 设计对话中 Agent 读代码事实并产出用例（含 code source/锚点）→ 确认用例 → 代码 Tab 触发索引/分析，查看事实/影响/锚点 → 配置视觉断言 → 执行首次 inconclusive → 管理员批准 actual 基线 → retest passed → 切换 fixture 页面主题 → retest failed（diff 证据）→ 质量页指标 → 代码/视觉/锚点/提议 UI 截图。

---

## 2. 实施策略、模块边界与仓库约束

### 2.1 模块与目录（按 Agent.md §6 + HLD §1.1）

- 新增 `backend/internal/codeintel/{domain,application,infrastructure,interfaces/httpapi}`：
  - `domain`：run/entity/edge/fact/analysis/candidate/anchor/review/limits/errors/fact_key。
  - `application`：git 端口、LLM 端口、worker、index/analysis/impact/anchor/review/proposal 服务、ports（version/design/resource/execution/eventing/audit/workqueue）。
  - `infrastructure`：`gitx`（只读 Git）、`postgres`（8 表仓储）、`llm`（ChatGateway 适配）、`eventing`、`audit`、`resource`/`version`/`design`/`execution` 端口适配。
  - `interfaces/httpapi`：`/api/code/*`、`/api/versions/{id}/code/*` handler/DTO。
- 新增 `backend/internal/visual/{domain,application,infrastructure,interfaces/httpapi}`：
  - `domain`：assertion config/baseline/diff/classification/limits/errors。
  - `application`：config/baseline/diff/overview 服务、ports（design case reader、execution evidence reader/result writer、eventing、audit）。
  - `infrastructure`：postgres（3 表）、eventing、audit、design/execution 端口适配。
  - `interfaces/httpapi`：`/api/versions/{id}/visual/*`、`/api/quality/visual-metrics`。
- 扩展 `execution`：visual 断言执行工具、证据 kind、`visual_skipped` 语义、intent code_baseline commit 升级、case-run summary 视觉计数。
- 扩展 `agent`：`code` 团队枚举、按团队 claim、codeintel 工具白名单、`execution.visual_assert`、`sut.page_propose`。
- 扩展 `version`：`version_sut.code_baseline_commit` 解析与 API 字段。
- 扩展 `resource`：page/API proposed 创建 + confirm/reject API；不改变已有 CRUD 语义。
- 扩展 `frontend/src/features/{code-intel,visual}`；修改 `versions`/`execution`/`design`/`clarification`/`delivery`/`resources`/`shell`/`quality` 的必要组件。
- 扩展 `executor`：`visual.compare` 动作进入协议（实现已在 `executor/src/visual.mjs`，按 0.2 证据/限额适配）。
- 扩展 `cmd/eztest` 装配：codeintel worker、visual 端口、HTTP 路由、settings seed；`server` 只做路由装配。

### 2.2 共享文件与写作用域

- **Lead 独占**：`docs/version-V0.2/contracts/*`、`backend/internal/db/migrations/*`+`order.txt`、`backend/cmd/eztest/main.go`/`p5_wiring.go`、`backend/internal/server/*`、settings catalog、agent profiles/工具注册/团队枚举、`scripts/p5-*`、handoff、spec/plan。
- 子代理/teammate 写作用域按 §15 离散：codeintel core、codeintel analysis/anchor、visual backend、executor、frontend code-intel、frontend visual、acceptance。
- 共享 `types.go`/端口由 Lead 先落；出现 `FS_STALE_VERSION` 重读 rebase，不并发改 contracts/migrations/cmd/server/seed。
- 每流 TDD、小步提交；Lead 合并每流前跑 `go build ./...` + 该流测试。

### 2.3 契约先行顺序

1. 先写 contracts 增量：db-schema §11–§13、migrations-order §7、openapi P5、events §14、task-types §7、agent-profiles-tools §8、settings §4e、executor-protocol §7/§8（按实际章节）、contracts README。
2. 再写迁移 #25–#31；`migrate_test.go`/`migrate_integration_test.go` 期望值改为 31（或 `>=31` + P5 tail）。
3. 再落共享类型/端口/装配骨架。
4. 最后并行实现各流；契约变更先改文档再改代码。
5. 禁止“代码先行、契约后补”。

### 2.4 真实 LLM 原则（全阶段强制）

- 验收脚本只用真实 DeepSeek（`.env` 的 `llm_*`），不用 fake 顶替；真实 LLM 失败视为系统缺陷（上下文/提示词/工具/scope/校验），按根因修复后重跑。
- fake 仅用于单测/集成的端口 stub；P5 不新增 fake-LLM 验收场景。
- T1 事实/影响若 LLM 不可用，产品行为是 `done + degraded`（T0-only）；验收的正常路径必须真实 LLM。降级路径用单测/集成 stub 覆盖。
- 视觉比对不使用 LLM；真实 Chrome + 固定 fixture 页面。

### 2.5 代码规范自检（P5 强制）

- `Agent.md` §6.1 四层齐全；不新建 `utils/common/helpers`；共享纯逻辑进 `platform`。
- §6.2 导入方向；跨上下文只依赖 application 端口。
- §6.3 迁移只追加；一个文件一个上下文；启动 advisory lock/checksum；无 down migration。
- §6.4 前端只走 `api-client.ts`/`sse-client.ts`；业务在 `features/*`。
- §6.5 新上下文四层 + `cmd/eztest` 装配 + 迁移 + 前端 feature；worker 由组装根启停。
- §6.7 雪花 `BIGINT`；JSON/URL/工具参数 ID 为十进制字符串；禁 `BIGSERIAL`。
- §6.8 文件字节入内容账本；不写 PG 二进制；不暴露路径；鉴权下载。
- §6.9 表名/字段/索引命名；审计列；跨上下文无 FK；CHECK 全枚举。
- 每个 handler 只做 DTO/错误映射；服务端校验所有 AI 候选。

---

## 3. 数据模型与迁移（只追加 #25–#31）

### 3.1 通用规则

- 新表雪花 `BIGINT` 主键（应用生成，无 default/serial）；JSON/URL/工具 ID 为十进制字符串。
- 跨上下文只逻辑引用，不建 FK；同上下文 FK 显式命名。
- 审计列：`created_at TIMESTAMPTZ NOT NULL DEFAULT now()`、`updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`；append-only 表 `updated_at` 仅插入初始化（仓储不提供更新）。
- 枚举全 CHECK；JSONB 列 `NOT NULL DEFAULT` + `jsonb_typeof` CHECK。
- 索引命名 `pk_/uk_/idx_`；表名单数小写。
- 字符上限用 CHECK；路径/事实 key/原因等上限见各表。
- 迁移文件顺序追加到 `order.txt` 末尾：`codeintel_0001` #25、`codeintel_0002` #26、`codeintel_0003` #27、`visual_0001` #28、`execution_0004` #29、`version_0006` #30、`agent_0004` #31。

### 3.2 `codeintel_0001_code_index.sql`（order #25，codeintel 上下文，3 表）

**`code_index_run`**（T0 索引运行）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_index_run` |
| `version_id` | BIGINT | NULL（触发版本，逻辑引用 version；全局后台索引 P6/P7 不建） |
| `repo_id` | BIGINT | NOT NULL（逻辑引用 resource.code_repository） |
| `commit_sha` | TEXT | NOT NULL，CHECK `~ '^[0-9a-f]{40}$'` |
| `branch` | TEXT | NOT NULL DEFAULT ''，CHECK ≤255 |
| `source` | TEXT | NOT NULL DEFAULT 'git'，CHECK `git` |
| `status` | TEXT | NOT NULL DEFAULT 'queued'，CHECK `queued/running/done/failed` |
| `files_total` | INT | NOT NULL DEFAULT 0，CHECK ≥0 |
| `files_indexed` | INT | NOT NULL DEFAULT 0，CHECK ≥0 |
| `truncated` | BOOL | NOT NULL DEFAULT false |
| `stats` | JSONB | NOT NULL DEFAULT '{}'，CHECK object |
| `errors` | JSONB | NOT NULL DEFAULT '[]'，CHECK array |
| `started_at`/`finished_at` | TIMESTAMPTZ | NULL |
| `created_by` | BIGINT | NULL（逻辑引用 identity） |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_index_run`；partial unique `uk_code_index_run_active UNIQUE (repo_id) WHERE status IN ('queued','running')`；`idx_code_index_run_repo (repo_id, created_at DESC)`；`idx_code_index_run_version (version_id, created_at DESC)`。`errors` 应用限 ≤20 条。

**`code_entity`**（当前可查询实体/事实投影）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_entity` |
| `repo_id` | BIGINT | NOT NULL |
| `commit_sha` | TEXT | NOT NULL，40 hex |
| `index_run_id` | BIGINT | NULL（同上下文 FK `REFERENCES code_index_run(id) ON DELETE SET NULL`） |
| `kind` | TEXT | NOT NULL，CHECK `file/module/route/api/client_call/component/page/symbol` |
| `path` | TEXT | NOT NULL，CHECK 1..4096 |
| `name` | TEXT | NOT NULL DEFAULT ''，CHECK ≤512 |
| `signature` | TEXT | NOT NULL DEFAULT ''，CHECK ≤2000 |
| `start_line`/`end_line` | INT | NULL；CHECK `start_line IS NULL OR start_line>=1`；`end_line IS NULL OR (start_line IS NOT NULL AND end_line>=start_line)` |
| `blob_hash` | TEXT | NOT NULL DEFAULT ''，CHECK `'' OR git-sha1:<40hex> OR sha256:<64hex>` |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT 'high'，CHECK `low/medium/high` |
| `evidence` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤8 条应用限） |
| `meta` | JSONB | NOT NULL DEFAULT '{}'，CHECK object |
| `fact_key` | TEXT | NOT NULL DEFAULT ''，CHECK ≤512 |
| `fact_version` | INT | NOT NULL DEFAULT 1，CHECK ≥1 |
| `analysis_id` | BIGINT | NULL（逻辑引用 code_analysis） |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_entity`；partial unique `uk_code_entity_file UNIQUE (repo_id, commit_sha, path) WHERE kind='file' AND source='git'`；partial unique `uk_code_entity_fact UNIQUE (repo_id, commit_sha, fact_key) WHERE fact_key <> ''`；`idx_code_entity_kind (repo_id, commit_sha, kind, path)`；`idx_code_entity_path (repo_id, path)`；`idx_code_entity_analysis (analysis_id)`。

**`code_edge`**（实体关系）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_edge` |
| `repo_id`/`commit_sha` | BIGINT/TEXT | NOT NULL；40 hex |
| `analysis_id` | BIGINT | NULL |
| `src_entity_id`/`dst_entity_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES code_entity(id) ON DELETE CASCADE` |
| `rel` | TEXT | NOT NULL，CHECK `contains/imports/defines/calls/renders/route_to_api` |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT 'high'，CHECK `low/medium/high` |
| `evidence`/`meta` | JSONB | NOT NULL DEFAULT '[]'/'{}' |
| `fact_key` | TEXT | NOT NULL DEFAULT ''，CHECK ≤512 |
| `fact_version` | INT | NOT NULL DEFAULT 1，CHECK ≥1 |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_edge`；unique `uk_code_edge_src_dst_rel (src_entity_id, dst_entity_id, rel)`；`idx_code_edge_src (src_entity_id)`；`idx_code_edge_dst (dst_entity_id)`；`idx_code_edge_analysis (analysis_id)`。

### 3.3 `codeintel_0002_code_analysis.sql`（order #26，codeintel 上下文，3 表）

**`code_fact`**（不可变事实版本）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_fact` |
| `repo_id` | BIGINT | NOT NULL |
| `commit_sha` | TEXT | NOT NULL，40 hex |
| `fact_key` | TEXT | NOT NULL，CHECK 1..512 |
| `version` | INT | NOT NULL，CHECK ≥1 |
| `kind` | TEXT | NOT NULL，CHECK 同 `code_entity.kind` |
| `path` | TEXT | NOT NULL，CHECK 1..4096 |
| `name`/`signature` | TEXT | NOT NULL DEFAULT ''，CHECK ≤512/≤2000 |
| `start_line`/`end_line` | INT | NULL；同实体行号规则 |
| `blob_hash` | TEXT | NOT NULL DEFAULT ''，CHECK 同实体 |
| `body` | JSONB | NOT NULL DEFAULT '{}'，CHECK object |
| `evidence` | JSONB | NOT NULL DEFAULT '[]'，CHECK array |
| `source` | TEXT | NOT NULL，CHECK `git/ai` |
| `confidence` | TEXT | NOT NULL DEFAULT 'high'，CHECK `low/medium/high` |
| `model` | TEXT | NOT NULL DEFAULT ''，CHECK ≤128 |
| `prompt_version` | TEXT | NOT NULL DEFAULT ''，CHECK ≤64 |
| `analysis_id` | BIGINT | NULL（逻辑引用 code_analysis） |
| `superseded_at` | TIMESTAMPTZ | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_fact`；unique `uk_code_fact_key_version (repo_id, commit_sha, fact_key, version)`；`idx_code_fact_lookup (repo_id, commit_sha, fact_key, version DESC)`；`idx_code_fact_kind (repo_id, commit_sha, kind, path)`。

**`code_analysis`**（版本×仓库的变更分析运行）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_analysis` |
| `version_id` | BIGINT | NOT NULL |
| `repo_id` | BIGINT | NOT NULL |
| `base_commit_sha`/`head_commit_sha` | TEXT | NOT NULL，40 hex |
| `status` | TEXT | NOT NULL DEFAULT 'queued'，CHECK `queued/running/done/failed` |
| `degraded` | BOOL | NOT NULL DEFAULT false |
| `model` | TEXT | NOT NULL DEFAULT ''，CHECK ≤128 |
| `prompt_version` | TEXT | NOT NULL DEFAULT ''，CHECK ≤64 |
| `analysis_version` | INT | NOT NULL DEFAULT 1，CHECK ≥1 |
| `changed_files` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤2000 应用限；元素 `{path,old_path,status,additions,deletions,binary,head_blob_hash}`） |
| `stats` | JSONB | NOT NULL DEFAULT '{}'，CHECK object |
| `unmapped` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤200 应用限；`{path,reason}`） |
| `errors` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤20） |
| `fact_versions` | JSONB | NOT NULL DEFAULT '{}'，CHECK object（fact_key→version 快照，≤500） |
| `created_by` | BIGINT | NULL |
| `started_at`/`finished_at` | TIMESTAMPTZ | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_analysis`；partial unique `uk_code_analysis_active UNIQUE (version_id, repo_id) WHERE status IN ('queued','running')`；`idx_code_analysis_version (version_id, repo_id, id DESC)`；`idx_code_analysis_status (status, id)`。

**`code_impact_candidate`**（影响候选）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_impact_candidate` |
| `analysis_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES code_analysis(id) ON DELETE CASCADE` |
| `subject_kind` | TEXT | NOT NULL，CHECK `requirement/requirement_version/case_version/sut_page/sut_api` |
| `subject_id` | BIGINT | NOT NULL |
| `reason` | TEXT | NOT NULL DEFAULT ''，CHECK ≤2000 |
| `confidence` | TEXT | NOT NULL，CHECK `low/medium/high` |
| `source` | TEXT | NOT NULL，CHECK `direct/contract/relation/ai` |
| `evidence` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤5） |
| `selected` | BOOL | NOT NULL DEFAULT true |
| `selection_updated_by` | BIGINT | NULL |
| `selection_updated_at` | TIMESTAMPTZ | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_impact_candidate`；unique `uk_code_impact_candidate_subject (analysis_id, subject_kind, subject_id)`；`idx_code_impact_candidate_subject (subject_kind, subject_id)`。

### 3.4 `codeintel_0003_anchor_review.sql`（order #27，codeintel 上下文，2 表）

**`code_anchor`**（统一代码锚点）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_anchor` |
| `version_id` | BIGINT | NOT NULL |
| `repo_id` | BIGINT | NOT NULL |
| `subject_kind` | TEXT | NOT NULL，CHECK `version/requirement/requirement_version/case_version/issue` |
| `subject_id` | BIGINT | NOT NULL |
| `file_path` | TEXT | NOT NULL，CHECK 1..4096 |
| `start_line`/`end_line` | INT | NULL；同实体行号规则 |
| `entity_id` | BIGINT | NULL（逻辑引用 code_entity） |
| `commit_sha` | TEXT | NULL，CHECK 40 hex |
| `excerpt_sha` | TEXT | NOT NULL DEFAULT ''，CHECK `'' OR sha256:<64hex>` |
| `status` | TEXT | NOT NULL DEFAULT 'valid'，CHECK `valid/stale/broken` |
| `confidence` | TEXT | NOT NULL DEFAULT 'high'，CHECK `low/medium/high` |
| `created_by_kind` | TEXT | NOT NULL DEFAULT 'user'，CHECK `user/ai/system` |
| `created_by` | BIGINT | NULL |
| `last_validated_at` | TIMESTAMPTZ | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_code_anchor`；unique `uk_code_anchor_subject_point (version_id, subject_kind, subject_id, file_path, COALESCE(start_line,0), COALESCE(end_line,0))`；`idx_code_anchor_subject (subject_kind, subject_id)`；`idx_code_anchor_repo_path (repo_id, file_path)`。

**`code_fact_review`**（append-only 事实复核）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_code_fact_review` |
| `fact_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES code_fact(id) ON DELETE CASCADE` |
| `fact_version` | INT | NOT NULL，CHECK ≥1 |
| `repo_id` | BIGINT | NOT NULL |
| `commit_sha` | TEXT | NOT NULL，40 hex |
| `verdict` | TEXT | NOT NULL，CHECK `correct/incorrect` |
| `reason` | TEXT | NOT NULL DEFAULT ''，CHECK ≤2000 |
| `reviewer_id` | BIGINT | NOT NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（`updated_at` 仅插入初始化） |

约束/索引：`pk_code_fact_review`；`idx_code_fact_review_fact (fact_id, id DESC)`；`idx_code_fact_review_created (created_at DESC)`。

### 3.5 `visual_0001_visual_assertion.sql`（order #28，visual 上下文，3 表）

**`visual_assertion`**（视觉断言配置）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_visual_assertion` |
| `version_id`/`case_version_id`/`case_assertion_id` | BIGINT | NOT NULL |
| `step_seq`/`assertion_seq` | INT | NOT NULL，CHECK >0 |
| `version_sut_id` | BIGINT | NOT NULL |
| `selector` | TEXT | NOT NULL DEFAULT ''，CHECK ≤500 |
| `region` | JSONB | NOT NULL DEFAULT '{}'，CHECK object（`{x,y,width,height}` 或空） |
| `threshold` | NUMERIC(6,5) | NOT NULL DEFAULT 0.005，CHECK 0..0.1 |
| `viewport_width`/`viewport_height` | INT | NOT NULL DEFAULT 1280/720，CHECK 1..10000 |
| `device_scale_factor` | NUMERIC(4,2) | NOT NULL DEFAULT 1.0，CHECK 0.5..4 |
| `wait_policy` | JSONB | NOT NULL DEFAULT '{}'，CHECK object |
| `config_revision` | INT | NOT NULL DEFAULT 1，CHECK ≥1 |
| `status` | TEXT | NOT NULL DEFAULT 'active'，CHECK `active/archived` |
| `created_by` | BIGINT | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_visual_assertion`；unique `uk_visual_assertion_case_assertion (case_assertion_id)`；`idx_visual_assertion_version (version_id, status)`；`idx_visual_assertion_case_version (case_version_id)`；`idx_visual_assertion_version_sut (version_sut_id)`。

**`visual_baseline`**（已批准基线历史）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_visual_baseline` |
| `visual_assertion_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES visual_assertion(id) ON DELETE CASCADE` |
| `case_version_id`/`version_sut_id` | BIGINT | NOT NULL |
| `evidence_id` | BIGINT | NOT NULL（逻辑引用 execution.evidence） |
| `sha256` | TEXT | NOT NULL，CHECK 64 hex |
| `size_bytes` | BIGINT | NOT NULL，CHECK >0 |
| `width`/`height` | INT | NOT NULL，CHECK >0 |
| `viewport_width`/`viewport_height` | INT | NOT NULL，CHECK >0 |
| `device_scale_factor` | NUMERIC(4,2) | NOT NULL，CHECK 0.5..4 |
| `config_revision` | INT | NOT NULL，CHECK ≥1 |
| `environment_fingerprint` | TEXT | NOT NULL，CHECK 64 hex |
| `status` | TEXT | NOT NULL DEFAULT 'approved'，CHECK `approved/superseded` |
| `approved_by` | BIGINT | NOT NULL |
| `approved_at` | TIMESTAMPTZ | NOT NULL |
| `superseded_at` | TIMESTAMPTZ | NULL |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

约束/索引：`pk_visual_baseline`；partial unique `uk_visual_baseline_approved UNIQUE (visual_assertion_id) WHERE status='approved'`；`idx_visual_baseline_assertion (visual_assertion_id, id DESC)`；`idx_visual_baseline_evidence (evidence_id)`。

**`visual_diff`**（每次比对结果）

| 列 | 类型 | 约束/默认 |
|---|---|---|
| `id` | BIGINT | PK `pk_visual_diff` |
| `visual_assertion_id` | BIGINT | NOT NULL，同上下文 FK `REFERENCES visual_assertion(id) ON DELETE CASCADE` |
| `case_run_id`/`assertion_result_id` | BIGINT | NOT NULL |
| `baseline_id` | BIGINT | NULL，同上下文 FK `REFERENCES visual_baseline(id) ON DELETE SET NULL` |
| `actual_evidence_id` | BIGINT | NOT NULL |
| `diff_evidence_id` | BIGINT | NULL |
| `diff_ratio` | NUMERIC(8,6) | NOT NULL DEFAULT 0，CHECK 0..1 |
| `threshold` | NUMERIC(6,5) | NOT NULL，CHECK 0..0.1 |
| `classification` | TEXT | NOT NULL，CHECK `passed/failed/inconclusive/blocked` |
| `reason` | TEXT | NOT NULL DEFAULT ''，CHECK ≤2000 |
| `environment_match` | BOOL | NOT NULL DEFAULT true |
| `width`/`height` | INT | NOT NULL DEFAULT 0，CHECK ≥0 |
| `mismatch_boxes` | JSONB | NOT NULL DEFAULT '[]'，CHECK array（≤50） |
| `created_at`/`updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now()（append-only，`updated_at` 仅初始化） |

约束/索引：`pk_visual_diff`；unique `uk_visual_diff_assertion_result (assertion_result_id)`；`idx_visual_diff_run (case_run_id, id DESC)`；`idx_visual_diff_assertion (visual_assertion_id, id DESC)`；`idx_visual_diff_classification (classification, created_at DESC)`。

### 3.6 `execution_0004_visual_evidence.sql`（order #29，execution 上下文）

- `evidence.kind` CHECK 从 8 类扩为 10 类：`screenshot/snapshot/console/network/http/db/download/text/visual_actual/visual_diff`。
- 实现方式：`ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;` 再 `ADD CONSTRAINT chk_evidence_kind CHECK (kind IN (...10...))`；应用侧证据映射/视图枚举同步。
- 不新增列；不改变既有证据语义。
- `assertion_result.kind` 已含 `visual`，不扩。

### 3.7 `version_0006_version_sut_code_baseline.sql`（order #30，version 上下文）

- `ALTER TABLE version_sut ADD COLUMN code_baseline_commit TEXT NULL CHECK (code_baseline_commit IS NULL OR code_baseline_commit ~ '^[0-9a-f]{40}$');`
- 版本绑定创建/更新时解析；解析失败保留 NULL；分析 API 对 NULL 返回 `baseline_commit_missing`（422）。
- 不新增表；API `VersionSUT` 增加可选 `codeBaselineCommit`。

### 3.8 `agent_0004_code_team.sql`（order #31，agent 上下文）

- `work_item.team` CHECK 从 5 值扩为 6 值：`clarification/design/execution/delivery/steward/code`。
- 实现方式：`ALTER TABLE work_item DROP CONSTRAINT IF EXISTS work_item_team_check;` 再 `ADD CONSTRAINT chk_work_item_team CHECK (team IN (...6...));`
- `work_item.version_id` 保持 NOT NULL；codeintel 任务必须版本作用域（B1a）。
- `team_room_*` 的 team CHECK 不变（P5 不为 code 团队建频道）。

### 3.9 既有表使用（不 ALTER，除 #29–#31）

- `code_repository`（resource）：只读使用 path/branch/include/exclude；索引状态由 `code_index_run` 派生。
- `version_sut`：除 #30 新列外不改；`code_repo_id` 决定可分析的仓库。
- `case_assertion`/`case_source`：不改；P5 读取 visual kind/code source，锚点写 `code_anchor`。
- `execution_intent`/`case_run`/`assertion_result`/`evidence`：除证据 kind 外不改；`code_baseline` 语义升级在应用层。
- `work_item`：除 team CHECK 外不改；新增 type 由应用注册。
- `content_ref`/`content_object`：visual actual/diff 字节复用；`owner_kind` 保持 `evidence`（由 execution 写）。
- `delivery_issue_ref`：不改枚举。
- `sut_page`/`sut_api`：不改列；P5 允许写 `status=proposed`、`source=ai|code`（P0 服务需增量支持，见 §7）。

### 3.10 命名与结构规范自检（Agent.md §6.9/D34）

- 新表全小写单数；PK `pk_<table>`；UK `uk_<table>_<cols>`；IDX `idx_<table>_<cols>`；跨上下文无 FK；同上下文 FK 显式。
- 审计列齐全；append-only 表 `updated_at` 仅初始化；JSONB 有 `jsonb_typeof` CHECK。
- 不使用保留字；不使用 `tbl_`/`tb_`；布尔列 `is_` 前缀仅用于新布尔列？P5 新布尔列 `truncated`/`degraded`/`environment_match`/`selected` 沿用既有 P3/P4 命名口径（D34 对“is_ 前缀”的显式历史例外不扩大到 P5？）。**裁决**：P5 新表按 D34，布尔列使用 `is_` 前缀会与既有 `truncated/degraded/selected` 语义不一致；P5-D-02 记录为 D34 的显式例外（与 P0–P4 既有布尔列一致），handoff 说明。
- 迁移只追加；#25–#31 不改 #1–#24。

---

## 4. 契约：API / 事件 / 任务 / 工具 / 设置 / 执行器

### 4.1 通用约定

- 成功 `{data:...}`；列表 `{data:[],nextCursor?,hasMore?}`；错误信封沿用 P0（`code/message/request_id/retryable/details`）。
- ID 在 JSON/URL/工具参数中为十进制字符串；`event.seq`/游标为整数；`sha256` 为 64 hex。
- 时间 RFC3339；分页 `before`/`limit`（沿用 P3/P4）或 `cursor`（代码树/事实用 cursor；版本列表用 before/limit）。
- 权限：所有业务 API 需登录；管理员-only：视觉基线批准、页面/API 提议确认/驳回；其余登录用户可读/触发。
- 幂等：重复触发索引/分析返回已存在运行（200 reused）；锚点重复 409；基线批准重复/证据不合法 422。
- 禁止在事件/响应中返回代码正文、凭据、文件系统路径；代码正文只经 `code.read`/`file` 鉴权读取。

### 4.2 API 清单（OpenAPI `0.2.0-p5`，新增约 38 ops）

**代码仓库/索引（登录用户；版本归属校验）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 1 | `POST /api/versions/{versionId}/code/index` | `{repoId, commit?}` | 202 `CodeIndexRunView`；同 repo+commit done 复用 200；active 409 `index_in_progress`；repo 未绑定 422 `repo_not_bound` |
| 2 | `GET /api/versions/{versionId}/code/repos` | — | `CodeRepoStatusView[]`（绑定仓库、latest index、baseline commit） |
| 3 | `GET /api/code/repos/{repoId}/index-runs/latest` | — | `CodeIndexRunView`；404 |
| 4 | `GET /api/code/repos/{repoId}/index-runs/{runId}` | — | `CodeIndexRunView` |
| 5 | `GET /api/code/repos/{repoId}/tree` | `commit,path?,cursor?,limit?` | `CodeTreeEntryView[]` + cursor；≤200/page |
| 6 | `GET /api/code/repos/{repoId}/file` | `commit,path,startLine?,endLine?` | `CodeFileWindowView`；≤400 行/64 KiB；审计 |
| 7 | `GET /api/code/repos/{repoId}/diff` | `base,head,cursor?,limit?` | `CodeChangedFileView[]` + cursor；≤2000 |
| 8 | `GET /api/code/repos/{repoId}/diff/file` | `base,head,path` | `CodeFileDiffView`（patch≤256 KiB；binary/large/deleted 标志） |
| 9 | `GET /api/code/repos/{repoId}/facts` | `commit,kind?,path?,cursor?,limit?` | `CodeFactView[]` + cursor；≤100 |
| 10 | `GET /api/code/repos/{repoId}/search` | `commit,q,kind?,cursor?,limit?` | `CodeEntityView[]` + cursor；path/name 子串，≤50 |
| 11 | `GET /api/code/repos/{repoId}/commits/{sha}` | — | `CodeCommitView` |

**版本分析/影响（登录用户）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 12 | `GET /api/versions/{versionId}/code/changes` | `repoId,head?` | `CodeChangeSetView`（base=version_sut commit；预览不落库） |
| 13 | `POST /api/versions/{versionId}/code/analyses` | `{repoId,headCommit?,force?}` | 202 `CodeAnalysisView`；active 409 `analysis_in_progress`；同 base/head/version 复用 200 |
| 14 | `GET /api/versions/{versionId}/code/analyses/latest` | `repoId` | `CodeAnalysisView`；404 |
| 15 | `GET /api/versions/{versionId}/code/analyses/{analysisId}` | — | `CodeAnalysisDetailView`（含 candidates/unmapped） |
| 16 | `PATCH /api/versions/{versionId}/code/analyses/{analysisId}/candidates/{candidateId}` | `{selected}` | `CodeImpactCandidateView`；404/409 |
| 17 | `GET /api/versions/{versionId}/code/impact` | `repoId?` | 最新分析候选的合并视图（多仓 union，带 repo/SUT 标签） |

**锚点（登录用户）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 18 | `GET /api/versions/{versionId}/code/anchors` | `subjectKind?,subjectId?` | `CodeAnchorView[]` |
| 19 | `POST /api/versions/{versionId}/code/anchors` | `{subjectKind,subjectId,repoId,filePath,startLine?,endLine?,excerptSha?}` | 201 `CodeAnchorView`；重复 409 `anchor_duplicate`；主体/文件非法 422 |
| 20 | `POST /api/versions/{versionId}/code/anchors/{anchorId}/validate` | — | `CodeAnchorView`（status 重算） |
| 21 | `DELETE /api/versions/{versionId}/code/anchors/{anchorId}` | — | 204；404 |

**事实复核（登录用户）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 22 | `GET /api/versions/{versionId}/code/facts/{factId}/reviews` | — | `CodeFactReviewView[]` |
| 23 | `POST /api/versions/{versionId}/code/facts/{factId}/reviews` | `{verdict,reason}` | 201 `CodeFactReviewView`；fact 不属于版本绑定仓库 404/422 |

**页面/API 提议（生成登录用户；确认/驳回 admin）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 24 | `POST /api/versions/{versionId}/code/page-api-proposals` | `{repoId?}` | 200 `CodeProposalResultView`（`{pages:{created,skipped},apis:{...}}`） |
| 25 | `GET /api/versions/{versionId}/code/page-api-proposals` | — | `CodeProposalView[]`（proposed 资源 + source/evidence） |
| 26 | `POST /api/resources/pages/{pageId}/confirm` | — | `Page`（proposed→approved）；admin；403/409 |
| 27 | `POST /api/resources/pages/{pageId}/reject` | — | `Page`（proposed→rejected）；admin |
| 28 | `POST /api/resources/apis/{apiId}/confirm` | — | `SutApi`；admin |
| 29 | `POST /api/resources/apis/{apiId}/reject` | — | `SutApi`；admin |

**视觉（读/配置登录用户；基线批准 admin）**

| # | 方法/路径 | 请求 | 响应/状态 |
|---|---|---|---|
| 30 | `GET /api/versions/{versionId}/visual/assertions` | `caseVersionId?,versionSutId?` | `VisualAssertionView[]`（含 active baseline 摘要） |
| 31 | `PUT /api/versions/{versionId}/visual/assertions/{caseAssertionId}` | `{versionSutId?,selector?,region?,threshold?,viewport?,deviceScaleFactor?,waitPolicy?}` | `VisualAssertionView`；upsert；config_revision++；旧 approved 基线 superseded；非法 422 |
| 32 | `GET /api/versions/{versionId}/visual/assertions/{id}` | — | `VisualAssertionView` |
| 33 | `POST /api/versions/{versionId}/visual/assertions/{id}/baselines` | `{evidenceId}` | 201 `VisualBaselineView`；admin；证据/尺寸/config 不合法 422；旧 approved superseded |
| 34 | `GET /api/versions/{versionId}/visual/assertions/{id}/baselines` | — | `VisualBaselineView[]`（历史） |
| 35 | `GET /api/versions/{versionId}/visual/diffs` | `caseRunId?,caseVersionId?,classification?,cursor?,limit?` | `VisualDiffView[]` + cursor |
| 36 | `GET /api/versions/{versionId}/visual/diffs/{diffId}` | — | `VisualDiffDetailView`（evidence ids/ratio/boxes） |
| 37 | `GET /api/versions/{versionId}/visual/overview` | — | `VisualOverviewView`（基线覆盖率/通过率/inconclusive 原因/diff p50/p95/最近失败） |
| 38 | `GET /api/quality/visual-metrics` | `versionId?` | `VisualMetricsView`（全局或单版本聚合；最小质量页） |

**既有 API 增量（非新 path）**

- `VersionSUT` schema 增加可选 `codeBaselineCommit`。
- `CaseRunSummaryView` 增加 `visualInconclusive`/`visualFailed`/`visualPassed`；`visualSkipped` 保留兼容，P5 起为 0（或仅“未配置视觉”时计数），描述更新。
- `EvidenceView.kind` 枚举增加 `visual_actual`/`visual_diff`；`GET .../evidence/{id}/content` 支持。
- `Page`/`SutApi` 响应已有 status/source；P5 允许 proposed/ai/code 值。
- `DesignAssertion` 不变；视觉配置走 P5 visual API，不改 P2 case 模型。

### 4.3 事件（events.md P5 增量，12 个）

channel：`version:{version_id}`（索引/分析/视觉）；索引若 `version_id` 为空则 `system`（P5 触发总是有版本，故实际为 version）。

| 类型 | 关键 payload |
|---|---|
| `code.index_started` | `index_run_id, repo_id, commit_sha, branch` |
| `code.index_finished` | `index_run_id, status, files_total, files_indexed, truncated, stats` |
| `code.index_failed` | `index_run_id, error_code, message` |
| `code.analysis_started` | `analysis_id, repo_id, base_commit_sha, head_commit_sha` |
| `code.analysis_finished` | `analysis_id, status, degraded, changed_files, facts, candidates, unmapped` |
| `code.analysis_failed` | `analysis_id, error_code, message` |
| `code.impact_computed` | `analysis_id, candidates_by_source, candidates_by_kind, selected_count` |
| `code.anchor_validated` | `anchor_id, subject_kind, subject_id, status` |
| `code.fact_reviewed` | `fact_id, fact_version, verdict, reviewer_id` |
| `code.page_api_proposed` | `pages_created, pages_skipped, apis_created, apis_skipped`（IDs ≤20） |
| `visual.baseline_approved` | `baseline_id, visual_assertion_id, evidence_id, approved_by` |
| `visual.diff_created` | `visual_diff_id, visual_assertion_id, case_run_id, classification, diff_ratio, reason` |

- payload 不含代码正文、图片字节、文件路径（锚点事件可含 subject id/status，不含 path）。
- 与 `work_item.*` 事件并存；codeintel work item 的 created/claimed 事件由 P1 worker 发布。

### 4.4 任务、团队、Profile、工具（task-types.md §7；agent-profiles-tools.md §8）

**团队**：`work_item.team` 新增 `code`。P5 不为 code 团队建 team_room/digest；任务事件走 version 频道。

**任务类型**：

| 类型 | team | 输入 payload | 输出 | 去重键 |
|---|---|---|---|---|
| `index.repo` | `code` | `{repo_id, commit_sha, branch, index_run_id}` | `code_index_run` 终态 + 事件 | `code:index:{index_run_id}` |
| `code.analyze` | `code` | `{analysis_id, repo_id, base_commit_sha, head_commit_sha}` | `code_analysis` 终态 + candidates + 事件 | `code:analyze:{analysis_id}` |

- 两者 version_id = 触发版本；max_attempts=3；priority=50；重试幂等（同一 run 重跑不重复事实/候选）。
- `index.repo`/`code.analyze` 不映射 Agent profile；由 codeintel 专用 worker 领取；agent worker 只领取 `clarification/design/execution/delivery/steward`（排除 `code`）。
- 领取：`WorkQueue.ClaimForTeams(ctx, claimant, teams)` 增量；codeintel worker 用 `code`；agent worker 用其余 5 个。
- 失败：可重试错误重新 open；超过 max_attempts → dead，run 置 failed；non-retryable → failed/dead。

**Profile**：P5 不新增 Agent profile；只扩展现有 profile 的 `allowed_tools`：
- `context_scout`：`code.tree,code.read,code.search,code.facts,code.routes,code.apis,code.diff,code.impact,sut.page_propose`
- `case_author`：`code.facts,code.routes,code.apis,code.diff,code.impact,code.anchors`
- `failure_analyst`：`code.diff,code.facts,code.anchors`
- `design_planner`：`code.impact`
- `case_executor`：`execution.visual_assert`
- 其余 profile 不变；不允许 codeintel 写工具。

**工具**（11 个新增；read-only 除两个候选写）：

| 工具 | 参数 | 结果/限制 |
|---|---|---|
| `code.tree` | `{repoId?,commit?,path?,cursor?,limit?}` | 树条目 ≤50；无正文 |
| `code.read` | `{repoId?,path,commit?,startLine?,endLine?}` | ≤400 行/64 KiB；超限 head+truncated |
| `code.search` | `{repoId?,q,kind?,commit?,cursor?,limit?}` | path/name 子串 ≤50 |
| `code.facts` | `{repoId?,kind?,path?,commit?,cursor?,limit?}` | 事实摘要 ≤100；无正文 |
| `code.routes` | `{repoId?,commit?,limit?}` | route/page 事实 ≤100 |
| `code.apis` | `{repoId?,commit?,limit?}` | api/client_call 事实 ≤100 |
| `code.diff` | `{repoId?,base?,head?,path?,limit?}` | 变更文件摘要 ≤200；不返回 patch 正文 |
| `code.impact` | `{repoId?,analysisId?,limit?}` | 候选 ≤200 |
| `code.anchors` | `{repoId?,subjectKind?,subjectId?,limit?}` | 锚点 ≤200 |
| `execution.visual_assert` | `{assertionId}` | 服务端读取 case_run/assertion/config/baseline；返回 classification/reason/diff/evidence ids；幂等重放 |
| `sut.page_propose` | `{kind:"page"|"api",name,url?,routeTemplate?,method?,pathTemplate?,reason?}` | 只写 `proposed`；返回 created/skipped/ids；admin 后续确认 |

- 所有工具 schema 不含 `versionId`；运行时注入 scope；结果超 `agent.tool_result_max_bytes`（32 KiB）落 content_object，只回 head+sha+ref。
- `repoId` 是业务参数，不是版本 scope；服务端校验它属于当前版本的绑定仓库。若版本只绑定一个仓库，可省略；多个仓库且省略 → 422 `repo_ambiguous`（`code.impact` 可跨仓，不受此限）。
- 工具错误码：`invalid_argument/not_found/unavailable/state_conflict`；不返回代码正文/凭据/路径。
- `sut.page_propose` 只允许 Context Scout/Case Author；`execution.visual_assert` 只允许 Case Executor（按 case_run scope）。

### 4.5 设置（settings.md §4e，53→69）

| key | 类型 | 默认 | 范围/说明 |
|---|---|---|---|
| `codeintel.max_index_files` | number | 5000 | 1..50000；T0 索引文件上限，超出 truncated |
| `codeintel.max_changed_files` | number | 20 | 1..200；T1 分析文件上限 |
| `codeintel.max_file_bytes` | number | 65536 | 1024..1048576；T1 可读文件上限 |
| `codeintel.max_diff_bytes` | number | 262144 | 4096..1048576；按需 patch 上限 |
| `codeintel.max_excerpt_lines` | number | 40 | 1..200；事实证据摘录上限 |
| `codeintel.analysis_timeout_seconds` | number | 180 | 10..1800；分析硬超时 |
| `codeintel.max_concurrent_analyses` | number | 1 | 1..4；worker 并发 |
| `codeintel.prompt_version` | string | `p5.1` | 变化会产生新事实版本 |
| `codeintel.exclude_globs` | json | `["**/.git/**","**/node_modules/**","**/dist/**","**/build/**","**/vendor/**"]` | 路径排除，按 slash 归一化 |
| `visual.default_threshold` | number | 0.005 | 0..0.1；新配置默认 |
| `visual.baseline_max_bytes` | number | 5242880 | 64 KiB..20 MiB；基线/actual 上限 |
| `visual.default_viewport_width` | number | 1280 | 1..10000 |
| `visual.default_viewport_height` | number | 720 | 1..10000 |
| `visual.default_device_scale_factor` | number | 1.0 | 0.5..4 |
| `visual.compare_timeout_seconds` | number | 60 | 5..300；executor 动作超时 |
| `visual.wait_timeout_seconds` | number | 15 | 1..120；network/font/稳定等待上限 |

- 全部 DB 设置，无新增 env；secret 无；热加载由 settings 服务提供；`restart_required` 默认 false。
- 代码常量硬上限（MaxEvidencePerFact=8、MaxFactsPerFile=200、MaxEdgesPerFile=200、MaxFactKeyChars=512、MaxReasonChars=2000、MaxPathChars=4096、MaxChangedFilesStored=2000、MaxTreeEntriesPerPage=200、MaxReadLines=400、MaxRequirementsInPrompt=100、MaxAnalysisErrors=20、MaxFactVersionsSnapshot=500、MaxUnmappedEntries=200）来自 0.1 `limits.go`，设置只能降低不能突破。
- 视觉常量：threshold 0..0.1；viewport ≤10000；DPR 0.5..4；selector ≤500 runes；mismatch boxes ≤50；region 归一化。

### 4.6 执行器协议（executor-protocol.md P5 增量）

- protocol 版本 v1.2；新增 action：
  - `visual.compare`：请求 `{baseline?:{base64,sha256?,bytes}|null,selector,region?,threshold,viewport:{width,height},device_scale_factor,wait_policy?}`；`baseline` 为 null/缺省表示 capture-only（无基线首跑）；响应 artifact `actual_png` + `diff_png?` + `{diff_ratio,diff_pixels,total_pixels,width,height,environment_match,environment:{viewport_width,viewport_height,device_scale_factor,browser,version},mismatch_boxes}`。
  - 动作 body 上限从 1 MiB 提高到 `visual.baseline_max_bytes`（默认 5 MiB），仅对该 action；其他 action 保持 1 MiB。
  - `baseline` 字节由 API 从内容账本读取；执行器不访问文件系统/网络取基线。
- 证据 kind 映射：`visual.compare` 的 actual/diff 由 API 映射为 `visual_actual`/`visual_diff`；`screenshot` 仍为通用截图。
- 环境指纹：viewport/DPR + selector/region/config_revision + 浏览器名/版本；不一致 `environment_match=false`，不返回 diff 图。
- 等待策略：network idle + `document.fonts.ready` + 注入 CSS 冻结动画/过渡/caret + 两帧稳定；超时受 `visual.wait_timeout_seconds`/action timeout 限制。
- 失败：页面/导航/上下文错误走通用错误码；`visual.compare` 不把环境不一致当失败。
- 安全：host allowlist/只读页面/不得执行页面脚本外的高危动作；基线字节不落执行器磁盘。

### 4.7 OpenAPI 与冻结

- `openapi.yaml`：`info.version: 0.2.0-p5`；根 `x-frozen: P5`；新增 tags `Code`/`Visual`/`Quality`；新增上述 38 ops 与 schemas；既有 P0–P4 schema 仅可选字段/枚举扩宽。
- 破坏性变更：无。P0–P4 表/字段/迁移不改（除 #29–#31 的加法）；P0–P4 API 仅新增可选字段/枚举值。
- 冻结时点：真实 LLM + 真实 Chrome 验收通过、`scripts/test.ps1` 全绿、`p5-contract-check.cjs` 通过后；冻结后 P6+ 只能消费。
- `contracts/README.md` 记录 P5 计数：31 迁移、76 表、12 事件、2 任务/`code` 团队、11 工具、16 设置、10 证据 kind、6 work item team。

---

## 5. 代码智能：T0 / T1 / 索引 / 分析 / 影响

### 5.1 T0 确定性 Git 层

**只读约束**：仓库只读；禁止 `checkout`/`switch`/`worktree`/`stash`/写工作树；所有命令 argv 形式、无 shell；`GIT_OPTIONAL_LOCKS=0`；cwd=仓库根；单命令 10s 超时；不信任 DB/模型给的 commit，必须 `rev-parse --verify <ref>^{commit}`。

**仓库校验**：`code_repository.path` 必须是本地目录；`git --is-inside-work-tree`；`branch` 为空表示默认 HEAD；`include/exclude` 为 glob，按 slash 归一化，exclude 优先；默认排除 `codeintel.exclude_globs`；符号链接（mode 120000）/子模块（160000）标记 unsupported，不进入 T1。

**T0 索引**：
1. 解析 commit：显式 40 hex 或 `branch^{commit}`；不存在 → 422 `commit_not_found`。
2. `git ls-tree -r -t -l -z <commit>` 解析 `{mode,type,object,size,path}`；只保留 blob 文件；按 path 排序；最多 `codeintel.max_index_files`；超出 `truncated=true`。
3. 写 `code_entity(kind=file, source=git)`：path、name=basename、start/end=NULL、blob_hash=`git-sha1:<object>`、confidence=high；`files_total/files_indexed`。
4. 二进制/超限/非 UTF-8 文件仍建 file 实体（T0 只做元数据），但 T1 跳过并进 `unmapped`。
5. 重复索引同 repo+commit：若已有 done run 且未 force，API 返回 200 reused；否则新建 run。

**变更集**：
1. `git diff --raw -z --no-ext-diff --find-renames=50% <base> <head>` + `git diff --numstat -z`；解析 status `A/M/D/R<score>/C<score>/T`、old_path、additions/deletions、binary。
2. 读 `git ls-tree <head> <path>` 的 blob 得到 `head_blob_hash`（删除为 NULL）。
3. 存 `changed_files` ≤2000；超出 `stats.truncated=true`；base==head → 0 changed，不调用 LLM。
4. 按需 patch：`git diff --unified=3 <base> <head> -- <path>`；≤`codeintel.max_diff_bytes`；二进制/删除/超限返回空 patch + 标志。

**文件读取**：`git show <commit>:<path>`；拒绝绝对路径/`..`/反斜杠逃逸/symlink/submodule；严格 UTF-8；1-based 行窗口 ≤400 行、≤64 KiB；审计 `code.read`（repo/path/commit，不记正文）。

**错误码**：`repo_not_found`/`path_not_repo`/`git_unavailable`（503 retryable）/`commit_not_found`/`invalid_head`（非 base 祖先）/`path_not_found`/`path_not_file`/`path_escape`/`unsupported_content`/`binary_file`。

### 5.2 T1 AI 事实层

**输入**：仅变更集里可读文本文件；排除 deleted/binary/symlink/submodule/`>max_file_bytes`/非 UTF-8；排序 `has_anchor desc → path asc`；最多 `codeintel.max_changed_files`；其余进 `stats.skipped(not_analyzed)`。

**调用**：每文件一次 LLM（默认模型/`llm` 配置），prompt_version=`codeintel.prompt_version`；要求严格 JSON；单文件最多 200 facts/200 edges/8 evidence；超时受 `analysis_timeout_seconds` 约束。

**事实 key 归一化**：
- `route:<name>`、`page:<name>`、`api:<METHOD> <normalizedPath>`、`client:<METHOD> <normalizedPath>`、`component:<path>#<name>`、`symbol:<path>#<name>`。
- normalizedPath：去 query/hash；连续 `/` 合并；数字/UUID/长 hex 段替换为 `{id}`；空名/不可归一化丢弃并计 `stats.invalidFacts`。
- 同一文件重复 fact_key 合并（path 顺序稳定）。

**证据校验（服务端唯一真值）**：
- 模型只给 `{file,startLine,endLine}`；服务端从 commit 内容读取精确行，按 `\n` 切分、去尾部 `\r`、1-based 闭区间、以 `\n` 连接、无尾换行，计算 `excerptHash="sha256:"+hex`。
- 合法条件：文件在 head 是普通文件、`1≤start≤end≤totalLines`、`end-start+1≤max_excerpt_lines`、文件≤max_file_bytes；模型给的哈希/行号不信任。
- 非法：不存 evidence；事实降为 `low` + `meta.invalidEvidence`；计数 `stats.evidenceChecks/evidenceFailures`；错误只记 path/line/reason，不记正文。

**置信度**：LLM high/medium/low；缺证据/非法证据强制 low；low 以“线索”展示（`meta.clue=true`），不自动选中影响。

**版本与去重**：
- `code_fact` 存不可变版本：同 repo/commit/fact_key 语义 body 相同 → 复用当前版本（不新增）；不同 → `version+1`，旧行 `superseded_at=now()`；`code_entity` upsert 为当前投影。
- 文件级复用：若 `(repo,commit,path,blob_hash,model,prompt_version)` 已有事实，跳过 LLM，计 `stats.cacheHits`（不建 cache 表）。
- `code_analysis.fact_versions` 存 ≤500 个 `fact_key→version` 快照，供复现。

**跨栈契约**：`client_call` × `api` 仅按精确 `(method, normalizedPath)` 匹配，生成 `code_edge(rel=route_to_api)`，confidence=min(两侧)；一跳传播；每侧≤20 边；不满足精确匹配进 `unmapped(unmatched_contract)`。

**降级**：LLM 配置缺失/调用失败/JSON 解析失败 → analysis `done + degraded=true`，保留 T0 候选/unmapped，HTTP 2xx；`stats.aiUnavailable` 计数。

### 5.3 运行、work_item 与生命周期

**索引 API**：
1. 校验版本存在、repoId 属于该版本绑定的 `version_sut.code_repo_id`；否则 422 `repo_not_bound`。
2. 同 repo 已有 queued/running 索引 → 409 `index_in_progress`。
3. 同 repo+commit 已有 done 且未 force → 200 reused。
4. 否则创建 `code_index_run(queued)` + ensure work_item `index.repo`（dedupe `code:index:{index_run_id}`；run id 唯一，force 重索引不会与旧任务冲突），202。
5. 索引成功后若 `version_sut.code_baseline_commit` 为 NULL，回填为本次 commit。

**分析 API**：
1. 校验 repo 绑定；base = `version_sut.code_baseline_commit`（显式 `baseCommit` 不允许，P5 只允许分析版本基线→head）；NULL → 422 `baseline_commit_missing`。
2. head = 显式 40 hex 或当前 branch head；必须是 base 后代（`merge-base --is-ancestor`）；否则 422 `invalid_head`。
3. 同 version+repo 已有 queued/running → 409 `analysis_in_progress`。
4. 同 version+repo+base+head 已有 done 且未 force → 200 reused；否则 `analysis_version=max+1`，创建 `code_analysis(queued)` + ensure work_item `code.analyze`，202。
5. head==base → 直接 done，0 changed，不调 LLM。

**worker**：
- codeintel worker `ClaimForTeams(ctx, instance, []string{"code"})`；heartbeat 续租；`RecoverExpired` 每 3s；并发 `codeintel.max_concurrent_analyses`（索引固定 1）。
- claim 后 `MarkRunning`；执行 T0/T1/impact；成功 `Complete`；可重试失败按 P1 backoff；最终 dead → run `failed` + `errors`。
- run 状态与 work item 状态：queued→running→done/failed；重试不重置 run 为 queued；worker 每次开始时把 run 置 running。
- 启动恢复：只恢复过期租约，不批量中断；index/analysis 行保持 running，worker 重跑幂等。
- 取消：P5 不支持（API 无 cancel）；记 handoff backlog。

**状态与超时**：`analysis_timeout_seconds` 硬超时；超时→failed + work item retry；Panic 恢复→failed/重试；错误 JSON ≤20 条。

### 5.4 影响分析

**输入**：analysis 的 changed_files/facts/edges；`code_anchor`/`case_source`；版本下 requirement（非 obsolete）+ requirement_relation；current+confirmed case_version；版本绑定的 sut_page/sut_api。

**确定性候选**（按顺序）：
1. **direct 锚点**：`code_anchor`/`case_source` 的 path 在 changed_files；subject 对应 requirement/requirement_version/case_version/issue；`source=direct`，evidence=file path。
2. **direct 页面/API**：T1 的 `page/route` 事实与 `sut_page.route_template` 精确匹配；`api/client_call` 与 `sut_api.method+path_template` 精确匹配；subject=sut_page/sut_api。
3. **contract**：`route_to_api` 边连接变更的 client_call 与 api；一跳；subject=对应 sut_page/sut_api；confidence=min。
4. **relation**：直接受影响 requirement 经 `requirement_relation`（kind depends_on/conflicts_with/overlaps/shares_scope）一跳；subject=requirement/requirement_version；confidence=min(medium, 触发置信度)，不递归。
5. **case**：case_source 锚点命中 changed_files（direct）；受影响 requirement 下 current+confirmed case_version（relation）；subject=case_version。

**AI 推理**：一次调用，输入变更摘要 + 事实/边 + requirement 列表（≤100）+ 已选候选；输出候选 `{subject_kind,subject_id,reason,confidence,evidence}`；服务端校验 subject 存在/属于版本、evidence 在 head 合法、去重；未知 ID 丢弃；`source=ai`；失败则 degraded，不影响确定性候选。

**合并/排序/选择**：
- 按 `(subject_kind,subject_id)` 合并：source 优先级 `direct < contract < relation < ai`（数值低优先），取最高置信度；reason 拼接 ≤2000；evidence ≤5。
- 排序 `source → confidence(high>medium>low) → subject_kind(requirement<requirement_version<case_version<sut_page<sut_api) → subject_id`。
- `selected = confidence != low`；`PATCH` 可改；记录 actor/time。
- `unmapped` 每文件/原因一条：`deleted_file`/`unsupported_content`/`no_fact_or_anchor`/`ai_unavailable`/`unmatched_contract`；≤200。
- 多仓：每个 analysis 独立；`GET /impact` 合并并带 `repoId/versionSutId` 标签；不跨仓合并同一 subject 的 evidence（取最高优先级 + 合并 reason）。

**降级**：AI 不可用 → 确定性候选 + `unmapped(ai_unavailable)`，`degraded=true`，2xx。

### 5.5 代码基线（commit 升级）

- `version_sut.code_baseline_commit`：
  - 版本绑定创建/更新时，若 `code_repo_id` 非空，解析 `git rev-parse <branch>^{commit}`（branch 空=HEAD）；成功写列；失败/仓库不可读保留 NULL + 审计 warning；不阻塞版本创建。
  - 首次成功索引时，若列为 NULL，回填为索引 commit。
  - 分析要求该列非 NULL；否则 422 `baseline_commit_missing`，UI 提示先索引。
- `execution_intent.code_baseline`：
  - 新算法：对版本绑定中每个 `code_repo_id` 去重，排序 `repo_id`；每项 `{repo_id, commit_sha}`（有 commit）或 `{repo_id, branch}`（无 commit）；`sha256(canonical JSON)` 64 hex。
  - 全部无仓 → `""`；全部有 commit → 纯 commit 指纹；部分缺 → 混合指纹（可复现、可降级）。
  - `VersionBinding` 增加每 `version_sut` 的 `Commit` 字段；P3 `CodeBaseline` 函数/测试更新；P3 `P3-D-09` 的“P5 改为 commit”落地。
- 分析 base = 该 version_sut 的 commit；head = 当前 branch head；UI 可刷新 head；`code_analysis` 存 base/head。
- **同一 repo 绑定多个 version_sut**：版本绑定/解析必须得到同一 commit；若不同，分析返回 409 `repo_baseline_conflict`，UI 提示统一基线；`code_analysis`/`code_index_run` 按 repo 去重；`execution_intent` 的 code_baseline 也按 repo 去重。
- 回退：版本无仓/仓库无法解析 → intent 用 branch 哈希；分析 422；handoff 记录。

### 5.6 代码智能测试（最低集）

- gitx 单测：ls-tree/diff 解析（A/M/D/R/C/T、binary、rename、-z 注入）；base/head 祖先校验；路径逃逸；UTF-8/超限；10s 超时。
- 索引集成：31 迁移、file 实体唯一、index run 状态/active 唯一、truncated、重复复用、baseline 回填。
- T1 单测/集成（stub LLM）：fact_key 归一化、证据哈希/行号/非法降级、版本+superseded、文件级 cache、degraded。
- 影响单测/集成：direct/contract/relation/case、priority/low 未选、unmapped、多仓 union、候选 PATCH。
- worker：claim team=code、agent worker 不领取 code、lease 过期恢复、dead→run failed、超时、幂等。
- API 集成：38 ops 的 200/201/202/204/400/401/403/404/409/422/429/503；契约检查。
- 前端：code tab 组件/api/SSE/深链/空错载/truncated/degraded。

---

## 6. 锚点与事实复核

### 6.1 `code_anchor` 规则

**主体归属**：
- `version`：subject_id = version_id。
- `requirement`：requirement.version_id = version_id。
- `requirement_version`：requirement_version.requirement_id 属于版本。
- `case_version`：case_version 所属 case/scenario/requirement 属于版本。
- `issue`：delivery_issue.version_id = version_id。

**创建校验**：
1. 版本存在；repo_id 属于版本绑定的 `version_sut`。
2. `file_path` slash 归一化；拒绝绝对路径、`..`、反斜杠逃逸、symlink/submodule；≤4096。
3. commit_sha 默认该 version_sut 的 `code_baseline_commit`（可显式传 head/40hex）；必须 40 hex 且文件在该 commit 存在且为普通文件。
4. start/end 可空；非空时 `1≤start≤end≤totalLines`；`excerpt_sha` 可空；若 start/end 提供而 excerpt_sha 省略，服务端从该 commit 内容自动计算 `sha256:` 摘录哈希；非空时重算校验。
5. 唯一点（subject+path+start/end）重复 → 409 `anchor_duplicate`。
6. 写 `status=valid`、`last_validated_at=now()`、created_by；审计。

**校验**：`POST .../validate` 的目标 commit 取 `最近 done 索引 commit → 当前 branch head → anchor.commit` 的第一可用值：
- 目标 commit 上文件不存在/不是普通文件/行越界 → `broken`。
- `excerpt_sha` 非空且与目标内容不匹配 → `stale`。
- `anchor.commit` 非空且与目标 commit 不同，且 `excerpt_sha` 为空 → `stale`（代码已移动，无法证明未变）。
- 否则 `valid`；更新 status/last_validated_at；事件 `code.anchor_validated`。
- 校验失败不删除锚点；UI 显示状态。锚点是历史 provenance，stale 不等于删除。

**`case_source` 兼容**：
- 读取 design case detail 的 `sources(kind=code)`，在代码 Tab/用例详情展示为“设计来源锚点”；校验时同样计算 valid/stale/broken（只读，不改 P2 表）。
- 新用例锚点写 `code_anchor(subject_kind=case_version)`；不自动写 `case_source`；UI 合并展示两者。
- `delivery_issue_ref` 不改；问题锚点只用 `code_anchor`，问题详情 API 合并返回。

**API/UI**：§4.2 #18–#21；前端在需求详情、用例详情、问题详情提供列表/添加/校验/删除；路径 `file:line` 可复制/跳转代码窗口。

### 6.2 `code_fact_review` 规则

- 只有 `code_fact` 存在且属于版本绑定 repo/commit 时可复核；`POST` 校验 fact.repo_id/commit_sha 在版本绑定仓库中。
- `verdict=correct|incorrect`；reason ≤2000；reviewer_id=当前用户；append-only；允许同一 fact 多次复核，最新（id DESC）为有效结论。
- `incorrect` 的事实从自动影响候选中排除（按 fact_key 匹配）；`correct` 恢复；不影响历史 analysis 已落候选（只影响新分析/重新计算）。
- 事件 `code.fact_reviewed`；审计 `code.fact_review`（不记正文）。
- UI：事实面板显示复核状态/最新 verdict/原因；可复核；incorrect 灰显/标记；审计时间线。
- 测试：追加式、最新结论、incorrect 排除、审计、权限、fact 不属于版本 404/422。

---

## 7. 视觉断言

### 7.1 配置模型与规则

- 一条 `visual_assertion` 对应一条 `case_assertion.kind=visual` + 一个 `version_sut`；`case_assertion_id` 唯一。
- 配置字段：selector（空=整页 viewport）、region（归一化 `{x,y,width,height}`，空=全图）、threshold（0–0.1，默认 `visual.default_threshold`）、viewport（默认设置）、DPR（默认设置）、wait_policy（默认 networkidle+fonts+冻结动画）。
- 允许为 draft 或 confirmed case_version 配置；**只有 current+confirmed 执行**；draft 配置不执行。
- 配置更新：`config_revision++`；同一事务把该 assertion 当前 approved 基线置 `superseded`；需要重新批准。值未变化则不升版/不 supersede。
- 新 case_version 不自动继承配置；前端“复制上一版本配置”= GET 旧配置 + PUT 新 `caseAssertionId`；不新增 API。
- 校验：case_assertion 存在且 kind=visual；case_version 属于版本；version_sut 属于版本且与 assertion 的 target_version_sut_id 一致（空=case_version.default_version_sut_id）；selector ≤500；region 合法；threshold/viewport/DPR 范围。
- 错误：`case_assertion_not_found`/`assertion_not_visual`/`case_version_not_in_version`/`version_sut_not_bound`/`invalid_visual_config`/`state_conflict`。

### 7.2 基线生命周期（D-07 A）

1. **首次执行（无 approved 基线）**：
   - 执行器仍打开页面、等待、截图 actual；落 `visual_actual` 证据；
   - `visual_diff(classification=inconclusive, reason=missing_visual_baseline, environment_match=true, diff_ratio=0)`；
   - `assertion_result.kind=visual, verdict=inconclusive, reason=missing_visual_baseline`；
   - case_run 聚合为 `inconclusive(case)`；不产问题候选；`summary.visualInconclusive++`。
2. **批准基线（admin）**：
   - 证据必须是 `visual_actual`（不接受通用 `screenshot`，确保带 config revision/环境指纹），属于同一 version/case_version/assertion/version_sut；
   - PNG 可解码、尺寸>0、sha/size 匹配、≤`visual.baseline_max_bytes`；
   - `actual` 记录的 config_revision 必须等于当前配置 revision；否则 422 `visual_config_changed`；
   - 全页（selector 空）要求 width/height=viewport×DPR（取整）；selector 断言接受任意可解码尺寸并记录；
   - 写 `visual_baseline(status=approved)`，旧 approved 置 superseded（同事务，部分唯一索引保证）；
   - 审计/事件 `visual.baseline_approved`。
3. **复测**：用 P3 retest intent/API；新 case_run；视觉服务读 approved 基线 → executor compare → 结果。
4. **新用例版本**：新 assertion id → 无配置/基线；需重新配置+批准；旧基线保留历史。
5. **stale/非 current**：执行器不产生视觉结果；旧基线不删除；UI 标“历史/不可执行”。

### 7.3 执行集成与四态

**工具**：`execution.visual_assert {assertion_id}`（仅 Case Executor；scope 注入 case_run/case_version/version）。服务端：
1. 校验 case_run 存在且属于当前 wave/version；assertion 属于 case_version 且 kind=visual。
2. 读 `visual_assertion` 配置 + approved 基线 + 基线证据元数据。
3. 无基线：执行器 `visual.compare` 以 capture-only 模式抓 actual（baseline 空），落证据 → inconclusive(missing baseline)。
4. 有基线：读基线字节；执行器 `visual.compare` 抓 actual + 比对；返回 actual/diff/ratio/boxes/env。
5. 落 `visual_actual`（始终）与 `visual_diff`（比对执行时）；写 `visual_diff` 行；写 `assertion_result`（kind=visual，verdict/reason）；审计/事件。
6. 幂等：同一 `(case_run, assertion)` 重复调用返回已记录结果，不重复比对/不重复证据。

**四态映射**：

| case_run 结果 | 触发 | assertion verdict | failure_class/reason |
|---|---|---|---|
| `passed` | 基线存在、环境/配置匹配、diff_ratio ≤ threshold | passed | `visual_match` |
| `failed` | 基线存在、匹配、diff_ratio > threshold | failed | `failed(application)` / `visual_mismatch` |
| `inconclusive` | 无基线 | inconclusive | `case` / `missing_visual_baseline` |
| `inconclusive` | 环境指纹不一致 | inconclusive | `environment` / `visual_environment_mismatch` |
| `inconclusive` | 配置 revision 变化/基线失效 | inconclusive | `case` / `visual_config_changed` |
| `inconclusive` | 基线证据读取/sha/size/PNG 非法 | inconclusive | `case` / `visual_baseline_invalid` |
| `inconclusive` | actual 抓取失败/超限/非法 | inconclusive | `case` / `visual_actual_invalid` |
| `blocked` | 页面/导航/会话/执行器通用错误 | inconclusive | `environment/session` / 通用失败码 |

- `visual_diff.classification` 存四态；`assertion_result.verdict` 保持 P3 枚举（passed/failed/inconclusive）；blocked 通过 case_run/failure_class 表达，`visual_diff.classification=blocked` 供 UI/质量指标。
- **移除假通过**：P3 的“visual 跳过但 case 可 passed”删除。Case Executor 的 task context/prompt 必须列出当前 case_version 的 visual 断言并要求逐条调用 `execution.visual_assert`；完成后 finalizer 校验：若 case_version 有 visual 断言但无 assertion_result，case_run 置 `inconclusive(case, visual_assertion_not_run)`；`CaseRunSummaryView.visualSkipped` 保留字段但 P5 正常路径为 0；新增 `visualPassed/visualFailed/visualInconclusive`。
- 失败应用用例照常产 P3 失败候选 → P4 问题。

**环境指纹**：`sha256(canonical {viewport_width,viewport_height,device_scale_factor,selector,region,config_revision,browser_name,browser_version})`；与基线指纹比较；不一致 → 不比对、不产 diff，actual 仍落证据，classification=inconclusive(environment)。
**等待/稳定**：network idle + `document.fonts.ready` + CSS 冻结动画/过渡/caret + 两帧稳定；受 `visual.wait_timeout_seconds`/action timeout 限制；失败按 actual_invalid/inconclusive。
**区域/阈值**：region 先裁剪 baseline/actual 同区域再比对；threshold 是差异像素比例；pixelmatch 每像素颜色阈值固定 0.1；diff 图与裁剪区域同尺寸；mismatch boxes 32×32 网格≤50。

### 7.4 证据与存储

- `visual_actual`/`visual_diff` 均走 `execution.evidence` + `content_ref/content_object`；单条 ≤`execution.evidence_max_bytes`（5 MiB）与 `visual.baseline_max_bytes`；sha/size 由 API 重算；不写文件系统路径；下载走现有鉴权 endpoint。
- `visual_diff` 只存 evidence id/ratio/classification/boxes，不存图片字节。
- 基线引用 `visual_actual` 证据，不复制字节；基线保护由 P7 GC 消费 `content_ref`（P5 不做 GC）。
- 证据不可变；无更新/删除 API；视觉 baseline supersede 只改 baseline 状态，不改证据。

### 7.5 质量指标与 UI

- `GET /api/versions/{id}/visual/overview`：
  - `baselineCoverage`：active visual_assertion 中最近 approved 基线数/总数；
  - `assertionCounts`：passed/failed/inconclusive/blocked（按 visual_diff 最新/窗口）；
  - `passRate`：passed/(passed+failed)（无基线不计入失败）；
  - `inconclusiveReasons`：reason→count；
  - `diffRatioP50/P95`：有 diff 的样本分位数；
  - `recentFailures`：最近 N 条 failed（assertion/case_run/diff id/ratio）。
- 全局 `GET /api/quality/visual-metrics` 聚合同口径；最小 `/quality` 页展示卡片 + 版本切换；其余质量能力 P6。
- UI 执行 Tab `?view=visual`：按波次/用例分组、四态徽标、actual/diff 查看（复用 EvidenceViewer）、批准基线（admin 按钮）、retest 入口、缺失基线提示、配置变更提示。
- 用例设计详情：visual 断言配置编辑（selector/region/threshold/viewport/DPR/wait）、复制上一版本、保存；不改变 P2 断言确认语义。
- 代码 Tab 的视觉入口可跳执行 visual 子视图。

### 7.6 视觉测试

- 单测：配置校验/规范化、基线审批（证据归属/尺寸/PNG/sha/config revision/403/422/supersede）、四态映射、环境指纹、evidence 限额。
- executor：`visual.compare` pixelmatch/region/threshold/mismatch boxes/环境不一致/PNG 解码/body 限额；协议 v1.2。
- 集成：visual 3 表约束/部分唯一、baseline/visual_diff 写入、证据 content ledger、API 200/201/403/404/409/422。
- 执行集成：无基线 inconclusive、批准后 retest passed、改页面 failed、改 viewport inconclusive、重复工具调用幂等、visual 未运行不假通过。
- 前端：visual 子视图/审批/差异查看/质量卡/配置编辑器/四态徽章/空错载。
- 真实浏览器：fixture 页面 theme light/shifted；真实 Chrome；截图/DOM 稳定。

---

## 8. 页面/API 提议与质量页

### 8.1 提议来源与生成

- **代码事实**：`code_fact` 的 `page/route` → `sut_page`；`api/client_call` → `sut_api`；使用归一化 path/route_template；方法+path_template。
- **执行 network 证据**：读取版本下 `evidence.kind=network` 的已解析请求列表（P3 已落库），提取 host/path/method；仅当 host/URL 属于该 `sut_environment.base_url` 时提议；无 network 证据则只用代码事实。
- **目标资源**：`version_sut` → `sut_id` + `environment_id`；页面/API 归该 environment。
- **去重**：按 `uk_sut_page_template(environment_id, route_template)` / `uk_sut_api_route(environment_id, method, path_template)`：
  - 已 approved → 跳过（skipped）；
  - 已 proposed → 更新 source/evidence/last_seen（不重复插入）；
  - 已 rejected → 不自动重新提议（P5 需人工删除/重提议；记 backlog）。
- **写入**：`sut_page(status=proposed, source=ai)`；`sut_api(status=proposed, source=ai|code)`；字段 name/url/route_template/method/path_template/auth_role/module/schema_ref 由事实/证据填充；审计 `code.page_api_proposed`；事件。
- **触发**：`POST /api/versions/{id}/code/page-api-proposals` 同步执行（有界 ≤200），需要版本至少一个绑定仓库；返回 created/skipped；重复调用幂等。
- **权限**：任何登录用户可生成；**仅 admin** 可 confirm/reject（D-09 A1）；403/409；审计 actor。
- **工具**：`sut.page_propose` 允许 Agent 逐条写 proposed（同一去重/校验），不直接 approved；结果返回 created/skipped/ids。
- **不做**：live HTTP BFS、自动 approved、页面/API 变更自动 stale 用例（P6/设计团队评估）。

### 8.2 质量页

- `/quality` 从 `PlaceholderPage` 替换为最小 `VisualQualityPage`：读取 `GET /api/quality/visual-metrics`，展示基线覆盖率、通过率、四态分布、inconclusive 原因、diff p50/p95、最近失败；版本切换；loading/empty/error；不展示未实现的质量/评测指标（P6）。
- 质量页不新增一级导航（已有“质量”入口）；不实现 eval_run/eval_metric。
- 测试：指标计算/空态/错误/版本切换；API 集成。

---

## 9. 前端设计

### 9.1 Feature 与路由

- 新增 `frontend/src/features/code-intel/`：`CodeTab.tsx`（版本内“代码与变更”Tab 容器）、`RepositoryStatus`、`IndexRunStatus`、`CommitRangePicker`、`ChangedFilesPanel`、`DiffDrawer`、`FactsPanel`、`FactReviewDialog`、`ImpactPanel`、`UnmappedPanel`、`AnchorsPanel`、`ProposalPanel`、`api.ts`、`types.ts`、测试。
- 新增 `frontend/src/features/visual/`：`VisualView.tsx`（执行 Tab `?view=visual`）、`VisualAssertionList`、`VisualComparisonCard`、`BaselineApprovalDialog`、`VisualDiffViewer`、`VisualConfigEditor`（设计用例详情复用）、`QualityMetricsPanel`、`api.ts`、`types.ts`、测试。
- 修改：
  - `features/versions/VersionDetailPage`：`code` Tab 渲染 `CodeTab`（替换“P5 阶段提供”占位）；保留 8 Tab；`?tab=code` 深链。
  - `features/execution/ExecutionTab`：增加 `?view=visual` 子视图切换；保留现有波次/结果/证据；visual 视图读 `features/visual`。
  - `features/design/CaseDetailPanel`：kind=visual 时渲染 `VisualConfigEditor` + 锚点列表/校验；确认/编辑语义不变。
  - `features/clarification` 需求详情：锚点面板（列表/添加/校验/删除）。
  - `features/delivery/IssuesTab` 问题详情：锚点面板。
  - `features/resources/PageAPIPanel`：proposed 行 + “从代码生成提议” + admin confirm/reject 按钮；approved/proposed/rejected 过滤。
  - `src/app/(app)/quality/page.tsx`：渲染最小 `VisualQualityPage`，移除 PlaceholderPage。
  - `features/versions/types.ts`：`VersionSUT.codeBaselineCommit?`；`CaseRunSummaryView` 视觉计数。
- 不新增一级导航/Tab；知识仍只在全局；视觉不做独立路由（仅 `?view=visual`）。

### 9.2 代码与变更 Tab

- 顶部：绑定仓库选择（多 SUT 多仓）、当前 baseline commit、latest index 状态/时间、触发索引、触发分析、base→head、degraded/truncated 提示、刷新。
- 左栏：变更文件列表（状态 A/M/D/R/C/T、binary、additions/deletions）；点击打开 `DiffDrawer`（按需 patch，≤256 KiB，截断/二进制/删除提示，ESC/焦点陷阱）。
- 中栏：事实分组（file/route/page/api/client_call/component/symbol），confidence 徽标，low 灰显“线索”；点击事实打开文件窗口（`code.read` 的 API）高亮行；事实复核按钮/状态。
- 右栏：影响候选（requirement/requirement_version/case_version/sut_page/sut_api 分组，source/confidence/reason/evidence，勾选 PATCH，乐观回滚）、unmapped、锚点面板、提议面板。
- 空/错/载：未绑定仓库、无 baseline、未索引、无变更、无分析、LLM 未配置/degraded、截断；每态有 CTA；不编造数据。
- 深链：`?tab=code&repo=&analysis=&candidate=&anchor=&fact=`；URL 为真值。
- SSE：订阅版本事件，`code.*` 事件 debounce 刷新；不新增轮询（SSE 断线由 sse-client 处理）。
- 响应式：≥1280 两栏/三栏；<1024 堆叠；不新增全局 store。

### 9.3 执行 Tab 视觉子视图

- `?view=visual`：按波次/用例分组；每条视觉断言显示四态徽标、reason、config/viewport/DPR、baseline 状态、actual/diff 缩略图、diff_ratio、evidence 链接；点击打开 EvidenceViewer（复用）或 VisualDiffViewer。
- 批准基线：admin 可见；从最近一次 `visual_actual` 选择/确认；展示尺寸/sha/config revision/证据；批准后 SSE 刷新；非 admin 隐藏/403。
- 缺失基线/配置变更/环境不一致：明确提示 + “批准基线/更新配置/复测”。
- retest：调用 P3 retest API（版本/用例），跳转新波次/用例。
- 空态：无视觉断言、无执行、无基线的引导。
- 质量卡入口：链接 `/quality?version=`。

### 9.4 设计/需求/问题锚点

- 用例详情：`sources(kind=code)` + `code_anchor(case_version)` 合并展示 path:line/sha/status；添加/校验/删除；visual 配置在同一面板。
- 需求详情：`code_anchor(requirement/requirement_version)` 列表/添加/校验/删除；显示 valid/stale/broken。
- 问题详情：`code_anchor(issue)` 列表/添加/校验/删除；与现有 evidence/refs 分区。
- 状态颜色/文案：valid=正常、stale=内容已变、broken=文件/行失效；不隐藏失效锚点；审计时间线。

### 9.5 资源提议与质量页

- PageAPIPanel：status/source 过滤（approved/proposed/rejected）；proposed 行高亮；admin 显示 confirm/reject；“从代码生成提议”调用版本级生成；结果 toast（created/skipped）；409/422 内联错误。
- 质量页：版本选择器；基线覆盖率/通过率/四态/inconclusive 原因/diff p50/p95/最近失败；空态“暂无视觉数据”；错误重试；不展示 P6 指标。

### 9.6 前端测试

- 组件/交互：CodeTab、DiffDrawer、FactsPanel、ImpactPanel、AnchorsPanel、ProposalPanel、VisualView、BaselineApprovalDialog、VisualConfigEditor、QualityPage。
- api.ts：路径/参数/错误映射/ID 字符串；SSE hook：code.*/visual.* 事件 debounce；深链写回。
- 无障碍：抽屉/对话框焦点陷阱/ESC；按钮禁用态；表格/列表可键盘；不回归既有测试。
- 现有 66 files/548 tests 必须保持通过；新增测试按文件/功能计。

---

## 10. 事件 / SSE / 异步集成

- codeintel/visual 事件走 `version:{id}` channel；codeintel work item 的 `work_item.*` 事件已由 P1 worker 发布；不新增 channel。
- 前端 `useVersionEvents` 或 code/visual feature 的 hook 订阅；`code.index_*`/`code.analysis_*`/`code.impact_computed`/`code.anchor_validated`/`code.fact_reviewed`/`code.page_api_proposed`/`visual.baseline_approved`/`visual.diff_created` 触发 debounce 刷新。
- 异步 API 202：响应含 run/analysis id；UI 显示 queued/running + SSE；断线重连由 `sse-client.ts` 处理；不新增 EventSource/轮询。
- 事件 payload 不含代码正文/图片字节/路径/凭据；截断列表 ≤20/≤100。
- 失败事件包含稳定 error_code；UI 显示可重试提示；index 失败可重试；analysis 失败可重试。
- `work_item.created/claimed/dead` 事件可作为调试；不要求 UI 展示。

---

## 11. 配置、迁移与启动

- 不新增启动 env；`.env` 仍 4 项（`EZTEST_DATABASE_URL`/`EZTEST_API_PORT`/`EZTEST_WEB_PORT`/`EZTEST_EXECUTOR_PORT`）。DeepSeek 凭据继续由 `.env` 小写 `llm_*` 读取并写入 DB LLM 配置（既有 P0 行为），不是第 5 个 EzTest 启动 env。
- settings seed：16 个新键（§4.5）加入 catalog；启动迁移后幂等 seed；管理 UI 自动分类 `codeintel`/`visual`；secret 无。
- `cmd/eztest` 装配：
  - 初始化 codeintel context（gitx/postgres/llm/eventing/audit/ports）与 visual context（postgres/eventing/audit/ports）。
  - 创建 `WorkQueue`（P1）并注入 codeintel worker；agent worker 使用排除 `code` 的团队集合；两者随进程启停，错误日志不阻塞 HTTP 启动（worker 启动失败则 API 启动失败？P3 worker 启动失败会导致启动失败；P5 同）。
  - 路由挂载 `/api/code/*`、`/api/versions/{id}/code/*`、`/api/versions/{id}/visual/*`、`/api/quality/visual-metrics`、resource confirm/reject。
  - 跨上下文适配：codeintel -> version/design/resource/execution/eventing/audit/workqueue；visual -> design/execution/eventing/audit；execution -> visual（visual assert 工具/证据）；version -> codeintel（baseline resolver）；server 只装配。
- 启动顺序：config → migrate（31）→ seed settings → snowflake → storage/audit/eventing/settings → contexts → workers → server → graceful shutdown。
- 迁移校验：`order.txt` 31 项、每文件一次、checksum、advisory lock；`migrate_test`/`migrate_integration_test` 期望 31 且 P5 tail；重复启动 0 应用。
- 开发库迁移前备份；验收从零建库；回滚=回退代码 + 保留 additive 表/列（无 down migration）。

---

## 12. 演示与验收（真实 LLM + 真实 Chrome）

### 12.1 原则

- 验收只用真实 DeepSeek（`.env` `llm_*`）与真实 Chrome（Playwright 1.64.x executor）；fake 只用于单测/集成 stub。
- 真实失败视为系统缺陷（上下文/提示词/工具/scope/校验/执行器），按根因修复后重跑；不得用 fake/跳过掩盖。
- 独立 DB `eztest_v02_p5`；API 14345；Web 16870；executor 14347；fixture target 13338；目标库 `EZTEST_TEST_DATABASE_URL`。
- 所有证据写 `.tmp/p5-acceptance/`（日志、scenarios.txt、截图、HTML）；handoff 引用路径。
- 验收前先做单场景探针（索引、分析、设计 Agent 读码、视觉比对各 1 次）；探针失败先修系统，不直接跑全量。

### 12.2 环境与数据准备

- `scripts/fixture-p5-repo.ps1`：重建 `.tmp/p5-repo`，两个固定 commit（作者/日期/消息固定）：
  - commit A：`src/api/refund.ts`（client POST /api/refund）、`src/router.ts`（GET /orders）、`src/pages/OrdersPage.tsx`、`server/refund.go`（POST /api/refund）、`src/utils/format.ts`、`src/legacy/old.ts`、`assets/logo.png`（二进制）。
  - commit B：refund 增加 `reason` 参数；新增 `/orders/refund`；`OrdersPage` 调用变化；`format.ts` 变化（unmapped）；删除 `old.ts`；logo 更新（binary）。
  - 输出 `P5_FIXTURE_REPO/BASE_COMMIT/HEAD_COMMIT`；可重复运行；不污染工作树（`.tmp` gitignored）。
- `scripts/fixture-p5-target.cjs`：13338；页面 `/p5/orders`、`/p5/visual?theme=light|shifted`（shifted 改变颜色/布局/文本，产生真实 diff）；API `/p5/api/orders`；稳定 DOM/字体；network 请求供提议证据。
- 种子：通过 API/SQL 建 SUT（P5 Fixture）、environment（base_url `http://127.0.0.1:13338`）、页面/API、code_repository（`.tmp/p5-repo`，branch main）、version 绑定、requirement/case；视觉用例含 visual assertion；管理员/普通用户。
- 视觉基线：first run 产生 actual；admin 批准；retest；主题切换；viewport 变更；证据下载。
- 真实 LLM：design agent 读码场景用 design conversation；prompt 明确要求使用 code.* 工具；验证 agent tool 调用/产物。
- 清理：验收结束停止 API/Web/executor/fixture；不删除 `.tmp/p5-acceptance` 证据。

### 12.3 验收场景（脚本断言 + 证据）

| # | 场景 | 关键断言 |
|---|---|---|
| S0 | 前置/门禁 | `scripts/test.ps1` EXIT=0；31 迁移；P4 契约；真实 LLM key；executor healthz；fixture 可重建 |
| S1 | T0 索引 | 索引 commit A done；file 实体/tree/file window；二进制/超限/符号链接处理；索引 commit B；diff A..B 含 A/M/D/binary；patch 截断/删除/二进制标志；工作树无变化 |
| S2 | T1 事实（真实 LLM） | analysis A..B done 非 degraded；facts 含 route/api/client_call/page/component/symbol（至少 route+api+client）；evidence 合法；invalid evidence 降级单测；fact version/cache；stats |
| S3 | 影响分析 | candidates 含 direct/contract/relation/ai 至少各一（AI 可用时）；case_version 候选；low 未选中；PATCH 勾选；unmapped 可见；多仓 union |
| S4 | 设计 Agent 读码 | design conversation 真实 LLM 调用 `code.facts`/`code.routes`/`code.apis`（agent tool 记录/事件）；产出 case 带 code source 或锚点；不假读 |
| S5 | 锚点 | requirement/case_version/issue 锚点创建/查询/校验 valid；改文件行内容→stale；删文件→broken；重复 409；越权/非法 subject 422；删除 204 |
| S6 | 事实复核 | correct/incorrect 追加；incorrect 后重新分析该 fact_key 不进自动影响；审计 actor/time；权限 |
| S7 | 页面/API 提议 | 生成 created/skipped；proposed 行 source=ai/code；重复幂等；admin confirm→approved；reject→rejected；非 admin 403；network 证据来源可见 |
| S8 | 视觉无基线 | 确认 visual assertion；执行 first run inconclusive(missing_visual_baseline)；`visual_actual` 证据；case summary visualInconclusive=1；不产问题候选；不假通过 |
| S9 | 基线批准/复测 | admin 批准 actual→baseline approved；旧基线 superseded；非 admin 403；非法证据 422；retest→passed；actual+diff 证据 sha/size；diff_ratio=0 |
| S10 | 视觉差异 | fixture theme=shifted；retest→failed(application)；diff 证据/diff_ratio>threshold/mismatch boxes；问题候选/链路可追溯 |
| S11 | 环境/配置不一致 | 改 config viewport（或 executor viewport）→inconclusive(environment/config_changed)；不产 diff；不判 failed；重新配置/批准后可恢复 |
| S12 | 质量/基线/回归 | visual overview 指标正确；`/quality` 卡片；`version_sut.code_baseline_commit` 解析；repo head 变化→intent code_baseline 变化；分析 base/head；P1/P2 fake、P3/P4 真实回归；p5 契约 |
| E2E | 浏览器 | 代码 Tab（索引/事实/影响/锚点/复核/提议）、执行 visual（批准/差异/证据）、资源确认、质量页；截图+HTML；无 JS 错误/无控制台错误 |

### 12.4 浏览器 E2E

- `frontend/e2e/p5.spec.ts` + `scripts/p5-e2e.ps1`：启动隔离 API/Web/executor/fixture，Playwright 真实 Chrome；登录；建/选版本；走代码 Tab/visual/quality 路径；截图 `.tmp/p5-acceptance/screenshots/`；失败保留 trace。
- E2E 使用真实 API/DB；LLM 场景可用真实 DeepSeek（与 API 验收同配置）；不得用 fake 替代关键断言。
- 覆盖深链（`?tab=code&...`、`?view=visual`）、空/错/载、权限（admin/non-admin）、SSE 刷新。

---

## 13. 测试策略与门禁

### 13.1 命令与期望

| 门禁 | 命令 | 期望 |
|---|---|---|
| 全量 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` | EXIT=0；Go unit+integration 全包 ok；前端 lint/typecheck/test 全过 |
| P5 契约 | `node scripts/p5-contract-check.cjs` | OK：38 ops、31 迁移、11 表、12 事件、2 任务/`code` 团队、11 工具、16 设置、10 证据 kind、6 team |
| P4 契约兼容 | `node scripts/p4-contract-check.cjs` | PASS（前向兼容） |
| P3/P2 契约兼容 | `node scripts/p3-contract-check.cjs` / `p2-contract-check.cjs` | PASS |
| 迁移幂等 | `go test -tags=integration ./internal/db/...` + 二次启动 | 31 applied once；二次 0；P5 tail |
| P5 API/DB 验收 | `powershell.exe -File scripts/p5-acceptance.ps1 -PollTimeoutSeconds 900` | S0–S12 全 PASS；`.tmp/p5-acceptance/` |
| P5 浏览器 E2E | `powershell.exe -File scripts/p5-e2e.ps1` | 通过；截图/HTML；`.tmp/p5-acceptance/e2e.log` |
| P1 回归 | `powershell.exe -File scripts/p1-acceptance.ps1 -FakeLLMPort 13334` | PASS steps；`.tmp/p5/p1-regression.log` |
| P2 回归 | `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334` | PASS；`.tmp/p5/p2-regression.log` |
| P2 E2E | `powershell.exe -File scripts/p2-e2e.ps1` | 通过 |
| P3 真实回归 | `powershell.exe -File scripts/p3-acceptance.ps1` | PASS 16/16；`.tmp/p5/p3-regression.log` |
| P4 真实回归 | `powershell.exe -File scripts/p4-acceptance.ps1` | PASS 13/13；`.tmp/p5/p4-regression.log` |
| P4 E2E | `powershell.exe -File scripts/p4-e2e.ps1` | 通过；截图 |

- 允许把 `p1/p2` 回归的 fake 作为 P4 惯例；P5 自身验收必须真实 LLM。
- 若 P4 真实验收时间过长，可至少跑 p4 契约 + 关键 API/E2E，并在 handoff 说明；不得静默跳过。

### 13.2 覆盖重点（phase plan §5 Review Focus 映射）

1. **契约漂移** → Task 0/25；`p5-contract-check` + p2/p3/p4 前向兼容；表/事件/工具/设置计数；迁移 tail。
2. **多 SUT 身份混用** → code per `version_sut`/repo；visual per `version_sut`；S1/S3/S8 跨 SUT 断言；不使用裸 `sut_id` 执行域。
3. **上下文/事实丢失** → T1 证据校验、fact version、analysis snapshot；设计 Agent 工具结果 32 KiB 落盘；S2/S4。
4. **执行幂等/重跑/恢复** → visual assert 幂等；work item lease/recovery；baseline/retest；S8–S11；并发测试。
5. **旧假设回流** → 不引入 0.1 8 状态/批次/case_node/文件系统证据/1 张 visual 表/`visual_skipped` 假通过；handoff 对照 decision-log。

### 13.3 证据规范

- 每个验收场景输出命令/结果/关键 ID/耗时；失败保留原始响应/截图/日志；不写“已通过”空话。
- 契约检查输出计数；迁移输出 applied/0；真实 LLM 输出 provider/model/analysis ID/degraded；视觉输出 baseline/actual/diff evidence IDs/diff_ratio/classification。
- 浏览器截图命名 `p5-<scenario>-<step>.png`；HTML 报告保留；控制台/网络错误为失败。
- `.tmp/p5-acceptance/scenarios.txt` 记录 S0–S12 结果；handoff §3 引用。

---

## 14. 契约变更与冻结

| 类型 | 变更 | 是否 frozen | 影响阶段 |
|---|---|---|---|
| API | `openapi.yaml` `info.version: 0.2.0-p5`、`x-frozen: P5`；38 新 ops；tags Code/Visual/Quality；`VersionSUT.codeBaselineCommit`、`CaseRunSummaryView` 视觉计数、`EvidenceView.kind` 扩宽 | `P5` | P6+ 只增量 |
| 事件 | `events.md` §14：12 个 code/visual 事件 | `P5` | P6+ |
| 表 | `db-schema.md` §11–§13：11 新表 + `version_sut.code_baseline_commit` + `evidence.kind` 10 值 + `work_item.team` 6 值 | `P5` | P6+ |
| 迁移 | `codeintel_0001/0002/0003`、`visual_0001`、`execution_0004`、`version_0006`、`agent_0004`；`order.txt` 31 | `P5` | P6+ |
| 任务/团队 | `task-types.md` §7：`index.repo`、`code.analyze`；team `code` | `P5` | P6+ |
| 工具 | `agent-profiles-tools.md` §8：9 code + `execution.visual_assert` + `sut.page_propose`；5 个 profile 白名单 | `P5` | P6+ |
| 设置 | `settings.md` §4e：16 键，53→69；`codeintel`/`visual` 分类 | `P5` | P6+ |
| 执行器 | `executor-protocol.md` v1.2：`visual.compare`、body 限额、证据映射、环境指纹 | `P5` | P6/P7 |
| 其它 | `contracts/README.md` P5 冻结状态；`p5-contract-check.cjs`；p2/p3/p4 前向兼容 | — | — |

- **破坏性变更**：无。P0–P4 表/字段/迁移不改（除加法列/CHECK 扩宽）；P0–P4 API 仅新增可选字段/枚举值。
- **冻结时点**：真实 LLM + 真实 Chrome 验收通过、`scripts/test.ps1` 全绿、`p5-contract-check` 通过后；冻结后 P6+ 只能消费。
- **未交付移交**：完整质量中心/评测、搜索、待办、Agent 中心 → P6；PDF/GC/多实例/问题增强 → P7；live probe、视觉 mask/抗 flake 高级策略 → P6/P7/P8。

---

## 15. 并行执行映射

| 流 | 写作用域 | 依赖 | 验收入口 |
|---|---|---|---|
| Lead | contracts、migrations、shared types/ports、settings、agent team/claim/profiles/tools、`cmd/eztest`、`server`、contract-check、spec/plan/handoff | 无 | 契约检查/编译/集成 |
| W1 codeintel core | `codeintel/{domain,application,infrastructure,interfaces}/**`（T0/index/tree/file/diff/T1 facts/worker） | Lead 契约/迁移/ports | 索引/T1/worker 单测+集成 |
| W2 codeintel analysis | `codeintel/**/analysis*`、`impact*`、`anchor*`、`review*`、`proposal*` | Lead ports + W1 | 影响/锚点/复核/提议 |
| W3 visual | `visual/**`、`execution/**/visual*`、`executor/src/visual*`/协议适配 | Lead 契约/迁移/ports | 视觉单测/执行集成/executor |
| W4 frontend codeintel | `frontend/src/features/code-intel/**`、versions/code Tab 增量 | Lead OpenAPI | vitest/typecheck |
| W5 frontend visual/quality | `frontend/src/features/visual/**`、execution visual 子视图、design/clarification/delivery 锚点、resources 提议、quality 页 | Lead OpenAPI + W3 API | vitest/typecheck/E2E |
| W6 acceptance | `scripts/p5-*`、`scripts/fixture-p5-*`、`frontend/e2e/p5.spec.ts`、`.tmp/p5-acceptance/*` | Lead 契约/脚本骨架 | 验收/E2E |

- 依赖图：`Lead 契约+迁移+ports` → W1/W3 并行 → W2（依赖 W1）→ W4/W5（依赖 OpenAPI，可与 W1/W3 并行做 mock）→ Lead 集成 → W6 验收 → 冻结/handoff。
- 共享文件冲突：contracts/migrations/`cmd/server`/seed/tool registry 禁止子代理并发写；W1/W3 共享端口/类型由 Lead 先落；`FS_STALE_VERSION` 重读 rebase。
- 每流 TDD、小步提交；Lead 每合并一流先 `go build ./...` + 该流测试；最终由 Lead 跑全量门禁。
- 子代理任务必须携带：spec 对应章节、写作用域、接口签名、测试命令、禁止修改文件；完成回报证据。

---

## 16. P5-D 裁决表

| 编号 | 裁决 | 依据/说明 | 用户批准 |
|---|---|---|---|
| P5-D-01 | **根因只写 spec/handoff，不改 0.2 设计文档；P5 以 0.2 权威 + 0.1 参考重写** | `9173c52` 批量写入、phase-05 未回写、引用错配；HLD:5 蓝图定位；用户选 A | 2026-11-01 用户选择 A |
| P5-D-02 | **8 张代码表 + 3 张视觉表，不建 change_set/cache/fact_version** | HLD:226-227；D-02 A | 2026-11-01 用户选择 A |
| P5-D-03 | **work_item 任务板 + `code` 团队 + 版本作用域 + codeintel 专用 worker** | HLD:282；P1 work_item 约束；D-03 B1a | 2026-11-01 用户选择 B1a |
| P5-D-04 | **T0 Git + T1 AI 事实 + 服务端证据校验 + degraded；限制进 DB 设置** | phase-05:19/58；0.1 CI §3–§7；D-04 A | 2026-11-01 用户选择 A |
| P5-D-05 | **确定性 direct/contract/relation + AI 影响；含 case_version 候选** | 0.1 P4 §7；0.2 case 模型；D-05 A | 2026-11-01 用户选择 A |
| P5-D-06 | **统一 `code_anchor` + 追加式 `code_fact_review`；不改 issue_ref 枚举** | phase-05:19/46；`db-schema:545/845`；D-06 A | 2026-11-01 用户选择 A |
| P5-D-07 | **视觉基线执行后审批 + actual 候选；admin-only；新版本不自动复用** | phase-05:20/47；0.1 P8 §9.3 验收；D-07 A | 2026-11-01 用户选择 A |
| P5-D-08 | **executor `visual.compare` + visual_actual/visual_diff + 四态** | `executor/src/visual.mjs`；HLD:227；D-08 A | 2026-11-01 用户选择 A |
| P5-D-09 | **提议=代码事实+network 证据；admin confirm/reject；最小质量页** | phase-05:21-22；HLD:426/564；D-09 A1+Q1 | 2026-11-01 用户选择 A1+Q1 |
| P5-D-10 | **全量 UI + code.* 只读 + visual_assert + page_propose** | phase-05:21-23；HLD:341/564；D-10 A+T1 | 2026-11-01 用户选择 A+T1 |
| P5-D-11 | **`version_sut.code_baseline_commit` + intent commit 哈希；无仓回退 branch** | P3 spec:313/P3-D-09；D-11 A | 2026-11-01 用户选择 A |
| P5-D-12 | **P5 专用 fixture + 隔离 DB/端口 + 真实 LLM/Chrome；P1/P2 fake、P3/P4 真实回归** | phase-05:43-48；P4 验收模式；D-12 A | 2026-11-01 用户选择 A |
| P5-D-13 | **P5 新表布尔列沿用 `truncated/degraded/selected/environment_match` 命名，不强制 `is_` 前缀** | D34 对既有字段的显式例外延续；避免两套语义 | 设计裁决，随 spec 批准 |
| P5-D-14 | **`visualSkipped` 保留兼容但 P5 正常路径为 0；新增 visualPassed/visualFailed/visualInconclusive；视觉未运行不判 passed** | P3-D-39；phase-05:60；D-07/D-08 | 设计裁决，随 spec 批准 |
| P5-D-15 | **codeintel run 不提供 cancel；work item max_attempts=3；dead→run failed** | P1 任务板语义；P5 验收无 cancel | 设计裁决，随 spec 批准 |
| P5-D-16 | **提议 `sut_page.source=ai`、`sut_api.source=ai|code`；rejected 不自动重提** | 0.2 枚举；OD-7；D-09 | 设计裁决，随 spec 批准 |
| P5-D-17 | **质量页只做视觉指标占位；完整质量中心/评测 P6** | phase-05:27；HLD:564；D-09 Q1 | 2026-11-01 用户选择 Q1 |
| P5-D-18 | **P5 不做 live probe/视觉 mask/GC/PDF/多实例；移交表见 §14/§18** | phase-05 Out；P6/P7 | 2026-11-01 用户选择 A/范围 |

- 任何实现中新偏差：先在 §16 追加 `P5-D-19+` 并停止报告，不得静默改设计。

---

## 17. 风险、边界与回滚

1. **范围 XL**：代码智能 + 视觉 + 前端 + 工具 + 契约并行；用 Lead 收口、按流 TDD、每流可独立编译/测试；优先验收路径（S1–S11）。
2. **P1 改动风险**：`code` team CHECK/枚举、按团队 claim、agent worker 排除 code；必须有 P1 回归（worker/claim/lease）和迁移幂等；不得改变既有 5 团队任务语义。
3. **真实 LLM 波动**：设计 Agent 读码/影响 AI 推理可能超时/输出不合 schema；按系统缺陷修 prompt/tool/scope/校验；先探针再全量；不得 fake 验收。
4. **Git fixture/环境**：路径/权限/autocrlf/符号链接导致 SHA 不稳定；脚本固定作者/日期/LF、`.tmp` 隔离、工作树 clean；Windows 路径 slash 归一化。
5. **视觉稳定性**：字体/动画/网络/DPR 导致 flake；固定 fixture、冻结动画、network idle/fonts ready、两帧稳定；环境指纹不一致判 inconclusive 而非 failed；不支持 mask（P7）。
6. **证据体积**：baseline/actual/diff ≤5 MiB/条；diff 图压缩；超限 422/inconclusive；P7 GC 保护基线引用。
7. **契约漂移**：先契约后代码；p5 检查覆盖 38 ops/31 迁移/11 表/12 事件/2 任务/11 工具/16 设置；p2/p3/p4 前向兼容；migrate 测试 ≥31+tail。
8. **代码基线解析失败**：仓库不可读/分支不存在 → NULL + 回退 branch 哈希；分析 422；UI 提示；不阻塞版本创建。
9. **视觉基线审批鸡生蛋**：首跑 inconclusive 是设计；验收必须走“批准→retest”；UI 明确 CTA；不得自动基线。
10. **多 SUT/多仓**：代码 per repo、视觉 per version_sut；impact union 带标签；不得用裸 sut_id 执行域。
11. **P4 回归**：视觉证据 kind/team CHECK/claim 改动可能影响 P3/P4；跑 P3/P4 真实回归 + P2 E2E；失败按回归缺陷修复。
12. **工作树污染**：用户专用文件/dirty 文件不提交；禁止 `git add -A`；contracts/migrations 只由 Lead 写。
13. **回滚**：迁移 additive，无 down；回滚=回退代码 + 保留表/列/枚举扩宽；旧 P3/P4 代码在旧数据上可运行（新列可空/默认）；契约冻结后变更需用户批准 + 决策记录。

---

## 18. Handoff 要求（`phase-05-handoff.md`）

必须包含：

1. **完成范围**：逐项勾选 §1.2；W1–W6 实际交付；未实现项显式列出。
2. **契约变更**：§14 全表；冻结 `P5`；破坏性变更（应无）；P6+ 可依赖的路径/表/事件/任务/工具/设置/协议。
3. **测试与验收证据**：§13 命令、结果、日志/截图/HTML 路径；真实 LLM 场景矩阵；浏览器 E2E；P1–P4 回归结论；契约计数。
4. **已知缺口/偏差**：P5-D 偏差表（尤其 work_item/team、无 cache 表、无 cancel、visual 首跑 inconclusive、无 live probe/mask、质量页占位）；未修复问题；是否影响下一阶段。
5. **未交付实体移交表**：完整质量中心/评测、搜索、待办、Agent 中心 → P6；PDF/GC/多实例/问题增强 → P7；live probe/视觉 mask/抗 flake 高级策略 → P6/P7/P8；每项 owner/输入契约/验收建议。
6. **下一阶段输入**：P6 读代码事实/影响/锚点/视觉证据/质量指标/提议 API；P7 读 content ledger/基线/证据/GC 保护；P8 读全量回归脚本/能力矩阵。
7. **前置检查**：P5 contracts `frozen: P5`、31 迁移、P5 验收证据、未交付实体表；缺 owner 即停。
8. **根因/流程修复**：§0.5 摘要；phase-05/启动提示词引用链问题；P6–P8 同类风险（P6 D54–D58 不存在、P7 D32/D33 误引）。
9. **风险**：§17 裁剪为下一阶段关注项。
10. **停止声明**：P5 完成即停止，不自动进入 P6；等待用户验收/批准。

---

## 19. 规格自审记录

- **占位符**：无 TBD/TODO；所有 P5 实体有 owner/表/API/测试/验收口径；所有迁移/事件/工具/设置有名称/默认/计数。
- **一致性**：8+3 表与 §3 一致；work_item `code` team 与 §4.4/§14 一致；visual 四态与 §7.3 一致；code baseline 与 §5.5/§14 一致；D-01…D-18 与 §0.4/§16 一致；38 ops/31 迁移/12 事件/2 任务/11 工具/16 设置在 §4/§13/§14 相互引用。
- **范围**：只覆盖 P5；P6/P7 在 §0.7/§14/§18 显式移交；未提前实现质量/搜索/待办/Agent 中心/PDF/GC/多实例/live probe。
- **歧义**：视觉四态中 blocked 只在通用执行错误出现且 assertion verdict 仍用 inconclusive（§7.3 明确）；codeintel 无 cache 表但用 file-level fact reuse（§5.2 明确）；`code` team 不建 team_room（§4.4 明确）；提议确认 admin-only（§8.1 明确）。
- **0.1 参考边界**：0.1 只取设计语义/验收思路，不继承 8 状态/批次/mindmap/文件系统证据/1 张 visual 表；0.2 表/API/权限/证据/任务板为准。
- **契约冻结**：§14 明确 frozen P5；P0–P4 无破坏性变更；p2/p3/p4 前向兼容。
- **用户批准**：§0.4 记录 2026-11-01 逐项裁决；spec 审阅后再进入 `writing-plans`。
