# EzTest P8 体验完善与硬化 — 阶段设计规格

> **状态**：v1.0（2026-10-29，设计已确认，待用户审阅）
> **依据**：`docs/design/implementation-phases.md` P8、`system-blueprint.md` v2.3、`frontend-design.md` v0.3、P7 设计规格与 P7 最终验收记录。
> **技术基线**：HEAD `b86b057`（`dev` 分支）；Go 1.24 + PostgreSQL 17 + Next.js 16/React 19/Tailwind 4 + 独立 Node/Playwright executor。
> **合规**：参考本地 ARTEX（AGPL-3.0）设计思想，**不复制源码**；新增依赖必须 MIT/Apache-2.0 兼容。
> **关键修订**：本阶段显式修订 P6 “探索链路不引图库”的决策（见 D53），改用 `@xyflow/react` 12 作为画布引擎，保留 `chain-layout` 确定性布局、ARIA/键盘与现有数据层资产。

---

## 0. 一页结论

P8 把 EzTest 从“P0–P7 功能可跑”推进到“可长期使用/可扩展/可运维”，按 6 个可独立验收的波次交付：

1. **W0 基线护栏**：当前 HEAD 全量门禁、CI、工程清理、缺陷 #1 重建复验。
2. **W1 探索链路 v2**：服务端链路读模型 + React Flow 画布 + ARTEX 级视觉/交互 + 数据正确性修复。
3. **W2 全局壳与版本详情**：版本切换器、全局搜索、待办中心、脑图 Tab、代码与变更完整版、报告优化。
4. **W3 管理面与体验硬化**：Agent 中心、设置与审计查看器、移动导航、可访问性、空/错/重连、证据对比。
5. **W4 质量平台**：Agent 评测集与五项指标、代码事实质量与影响度量、视觉断言（D7）、知识沉淀（D4）。
6. **W5 非功能与最终交付**：限流/SLO、GC、备份恢复、多实例与执行器硬化、安全审计、文档、最终真实验收。

**显式移交新阶段 P9（业务扩展与生态）**：报告 PDF/HTML、问题重开/合并、非 PG 目标库、多用户协作、跨版本知识库、多实例运维编排（均为候选，P9 启动时先澄清）。**无设计或非目标项（外部工单、CI/工单/IM 集成、环境编排、多租户/RBAC、性能/安全/移动端测试产品能力）登记为“未排期/待产品决策”，不进 P9 范围**。P8 只做体验与硬化，不新增业务能力。

**退出标准**：SLO 达标；评测集跑出指标并作为回归门禁；四类文档齐备可交接；全量门禁 + 真实 LLM + 真实浏览器 + 备份演练通过。

---

## 1. 阶段目标、范围与退出标准

### 1.1 目标

1. **探索链路对齐 ARTEX 级视觉语法与画布交互**，同时修复 P7 遗留的数据完整性/实时性/深链缺陷。
2. **前端完整**：全局导航（版本切换器/搜索/待办）、版本详情增强、管理面完善、移动端最小适配、可访问性。
3. **质量平台**：评测集与指标、代码事实质量、影响分析漏报/误报度量、视觉断言、知识沉淀（版本内）。
4. **非功能硬化**：限流/SLO、长跑资源清理、备份恢复演练、安全审计、审计日志、多实例单活、执行器硬化。
5. **文档与部署**：部署/运维/用户/开发文档；Compose 持久化与代码仓挂载；新增 P9 阶段。

### 1.2 范围（纳入）

| # | 范围项 | 交付形态 |
|---|---|---|
| 1 | 探索链路 v2 | 新只读聚合端点 + React Flow 画布 + 视觉/交互/状态/深链/数据正确性 |
| 2 | 全局壳 | 版本切换器、全局搜索、待办中心（顶栏） |
| 3 | 版本详情 | 脑图回 Tab（保留全屏路由）、代码与变更完整版、报告优化 |
| 4 | 管理面 | Agent 中心（13 逻辑角色目录/配置/运行观察/质量指标/只读）、设置完善、审计查看器 |
| 5 | 体验硬化 | 移动导航、axe 可访问性、空/错/重连态、证据前后对比 |
| 6 | 质量平台 | 评测集与五项指标、代码事实质量与影响度量、视觉断言（D7）、知识沉淀（D4） |
| 7 | 非功能 | 全局限流、SLO 测量、GC/保留、备份恢复、安全审计、审计覆盖、多实例单活、执行器硬化、配置接线、报告快照 |
| 8 | 工程 | CI、依赖/许可治理、过时文档/文案清理、P9 阶段登记 |
| 9 | 验收 | 每波真实浏览器验收 + 最终真实验收记录与截图 |

### 1.3 明确不做（移交 P9）

**移交 P9 候选（有明确产品意图，均无阶段级设计，启动时先澄清）**：报告 PDF/HTML；问题重开/合并与跨批次语义去重增强；非 PostgreSQL 数据库目标；多用户协作（读写锁/评审）；跨版本知识库；多实例运维编排。

**未排期/待产品决策（无设计或非目标，不进 P9 范围）**：外部工单同步（仅 `issue.external_ref` 预留字段，无集成设计）；CI/工单/IM 集成与 Webhook；环境编排；多租户/复杂 RBAC；性能/安全/移动端测试（作为产品能力，无具体产品定义）。P2 可选项默认不做。

### 1.4 与 P7 的交接输入（必须承接）

1. P7 最终验收结论 `PASS WITH LIMITS` 及其限制：`codeRefs=0`、真实 LLM 与真实执行未同跑、`archived` 手工失败分析竞态、Docker 未实跑、全局中心筛选证据不足。
2. 探索链路缺陷 #1：`586e1d3` 已修复 limit 200→100，但现场旧产物仍可复现；P8 必须重建产物复验并修三类残留（截断、错误文案、跨层契约守卫）。
3. 探索链路深层缺陷：结果层默认 20 条截断且忽略 `hasMore`；case-run 列表 evidence 恒空；graph/requirements 不订阅 SSE；深链大多未落地；后端 N+1 读放大。
4. P7 遗留硬化项（用户确认纳入）：`version_change`/`code_changed`、证据 GC + 报告 stale 联动、多实例 leader/分布式锁、P6 执行器硬化、报告快照 `history_rewritten`、`EZTEST_ANALYSIS_MAX_CASES`/`EZTEST_SUPPLEMENT_MAX_CASES` 接线。
5. 当前 `.next` 生产构建早于修复提交，验收前必须重建。

### 1.5 退出标准（P8 DoD）

1. **SLO 达标**：版本总览首屏 P95 ≤ 2.5s；链路 Tab 可交互 P95 ≤ 2.5s（200 节点/400 边）；批次 8 并发用例 0 错误；代码变更集 API P95 ≤ 2s，完整分析在配置上限内完成；结果写入 `docs/acceptance/p8/slo.md`。
2. **评测集可作为回归门禁**：确定性模式五项指标达到阈值且不劣于基线；真实 LLM 指标单独报告。
3. **视觉断言可用**：基线审批、区域/全图比对、阈值、四态分类与差异证据；纳入评测集。
4. **知识沉淀闭环**：版本内提交→人工审核→批准→注入澄清/设计提示词可追溯；跨版本复用留给 P9。
5. **硬化项完成**：全局限流、GC dry-run 与引用保护、备份恢复演练、安全审计报告、多实例单活、执行器上限强制、配置接线、报告快照字段。
6. **文档齐备**：部署/运维/用户/开发四类文档 + README/OpenAPI/Compose 同步；P9 阶段已登记。
7. **真实验收**：全量门禁 + 真实 LLM + 真实 Chrome + 截图索引 + `docs/acceptance/p8/acceptance.md`；无未解释失败、无秘密泄漏、无 console error。
8. **每波门禁**：未通过不得进入下一波（可裁剪范围，不跳过门禁）。

---

## 2. 决策记录（D53–D69）

| # | 决策 | 理由 | 影响/约束 |
|---|---|---|---|
| **D53** | **探索链路画布改用 `@xyflow/react` 12**，保留 `chain-layout.ts` 输出坐标、保留 ARIA tree/roving focus/键盘与现有数据 hook。这是对 P6“不引图库”的显式修订。 | React Flow 提供成熟的 pan/zoom/fit、MiniMap、点阵 Background、自定义节点、带标签/箭头/动画的边、视口裁剪；ARTEX 同栈（Next 16/React 19/Tailwind 4）已验证；MIT 许可。 | 新增运行时依赖（仅链路 Tab 懒加载）；禁用 React Flow 自带键盘导航，保留自有 ARIA；spike 门禁；**纯手写 SVG 方案弃用，不回退**。 |
| **D54** | **新增服务端链路只读聚合端点** `GET /api/versions/{id}/exploration-chain`，服务端完成 latest attempt、issue→case/result、证据计数、覆盖缺口、影响用例与筛选；现有 4 个 REST 端点保留。 | 消除前端 4 快照拼接、N+1、静默截断与图层不一致；为画布提供稳定节点/边契约。 | 新 DTO/查询预算；不新增业务实体；权限与版本读一致。 |
| **D55** | **全局搜索采用跨上下文只读 Provider 聚合**，新增 `internal/search` 应用服务；各上下文提供 `SearchProvider` 适配器，不直接跨上下文读表。 | 遵守 DDD 导入边界；避免新上下文直接访问他域表。 | 4 类结果（版本/需求/用例/问题）；ILIKE；per-type 上限；登录用户可见。 |
| **D56** | **待办中心由只读聚合端点提供**，聚合待评审、待接管、待升级 gate；管理员可见接管，普通用户只读可见评审。 | 顶栏需要一个稳定契约；避免前端跨页拼装。 | 30s 轮询 + 路由切换刷新；不做新审批模型。 |
| **D57** | **脑图回版本详情 Tab，同时保留 `/versions/{id}/mindmap` 全屏路由**；Tab 与全屏共享组件，`?tab=mindmap&case=` 深链。 | 满足 P8“回 Tab”，不破坏 P5 深链与全屏编辑体验。 | `MindmapPage` 增加 `embedded/initialCaseId`；Tab 顺序含脑图；不重复拉图。 |
| **D58** | **新增 `version_change` 表与 `code_changed` 事件**，按批次存代码变更快照；`/changes` 优先读快照，缺失时回退读时派生。 | P7 D50 明确 P8 建表；代码与变更完整版需要批次时间线与历史时点。 | 新迁移/事件；不影响既有 `/changes` 客户端。 |
| **D59** | **报告优化**：revision 选择器、D49 `baseFallbackReason/historyRewritten` 写入快照、证据计数纳入 `input_fingerprint` 并在证据变化时标 stale。 | 收口 P7 已知限制与 stale 联动。 | 报告生成/指纹/前端；PDF 仍留在 P9。 |
| **D60** | **Agent 中心 = 13 逻辑角色目录 + 现有 profile 配置 + 运行观察 + 质量指标**；新增只读 `/api/agent-center/*`，普通用户只读，写配置仍 admin-only。 | 文档要求普通用户只读；不放松 P1 admin-only 写接口。 | 报告/知识角色标注“确定性/人工审核，无独立 profile”，不伪造 profile。 |
| **D61** | **评测双轨**：确定性 fake LLM 模式作为回归门禁；真实 LLM 指标作为 gated 报告，不进入会抖动的 CI 门禁。 | 兼顾质量门禁稳定性与真实质量观测。 | `scripts/eval.ps1`；基线 JSON；指标定义见 §9.1。 |
| **D62** | **视觉断言新增 `visual` 断言类型 + 独立 `case_visual_assertion` 表 + 人工审批基线**；executor 用 MIT `pixelmatch` 比对；无基线/环境不符判 `inconclusive`，不假失败。 | D7 要求截图基线/区域比对/阈值/失败分类；保持核心断言表稳定。 | 新迁移/API/executor 依赖/UI；基线审批 admin。 |
| **D63** | **知识沉淀只做版本内闭环**：提交 → 人工审核 → 批准入库 → 注入本版本澄清/设计提示词；跨版本复用移交 P9。 | implementation-phases §5 明确跨版本知识库在 P8 之后。 | 新表/API/UI/prompt 注入；token 预算与可追溯。 |
| **D64** | **全局限流 + SLO 测量**：用户/IP 令牌桶、SSE 并发上限；SLO 脚本与阈值进入验收。 | P8 退出标准要求性能/并发/限流与 SLO。 | 新中间件/配置/脚本；429 + Retry-After。 |
| **D65** | **GC/保留策略**：孤儿文件、过期导出、终态批次证据按保留期清理；引用保护（发布报告、未关闭问题、最近 N 批、基线）与 dry-run。 | 长跑资源清理是 P8 明确项；必须防误删。 | 新配置/API/UI/审计；默认 dry-run。 |
| **D66** | **备份恢复**：`pg_dump` + 文件目录 + sha256 清单 + 恢复校验；本机演练并记录 RPO/RTO。 | 设计明确恢复演练纳入 P8。 | 脚本/文档；密钥丢失不可恢复需说明。 |
| **D67** | **多实例单活**：Postgres advisory lock 保护自主 worker 与执行调度；配置实例 ID；P6 执行器硬化（工具调用上限、WS 白名单、后台超时收尾）。 | P7 遗留硬化项；避免多实例重复建批/重复调度。 | 新锁/配置/集成测试；不改变单实例默认行为。 |
| **D68** | **安全审计与审计覆盖**：秘密/依赖/权限审计脚本 + 审计查看器 + 资源/Session/版本/脑图写操作审计补齐；安全响应头基线。 | P8 明确安全审计与审计日志。 | 不重架构认证（token 仍在 sessionStorage，记录为已知边界）。 |
| **D69** | **新增 P9 阶段**，只承接有明确产品意图的业务型候选；外部工单/CI-IM/环境编排/多租户/测试产品能力等无设计或非目标项登记为“未排期/待产品决策”，不进 P9；P8 只做体验与硬化，不新增业务能力。 | 用户确认的范围取舍；避免把“预留字段/历史备注”误当已设计能力。 | 同步 `implementation-phases.md`；未排期项若要做，先做需求澄清与集成设计。 |

---

## 3. 架构与模块边界

### 3.1 新增/变更模块

| 模块 | 类型 | 职责 |
|---|---|---|
| `backend/internal/chain` | 新只读读模型服务 | 链路聚合；定义 Provider 端口；不直接跨上下文读表 |
| `backend/internal/search` | 新应用服务（只读） | 全局搜索聚合；定义 `Provider` 端口；不直接跨上下文读表 |
| `backend/internal/workspace` | 新应用服务（只读） | 待办中心聚合；定义 `Provider` 端口 |
| `backend/internal/quality` | 新限界上下文 | 评测 run/指标持久化与查询；评测 runner 由脚本驱动 |
| `backend/internal/version` | 扩展 | 知识沉淀表/API/审核；链路读模型可放在 version 或独立 `chain` 应用服务 |
| `backend/internal/execution` | 扩展 | 视觉断言执行、证据 kind、执行器硬化、报告 stale 指纹扩展 |
| `backend/internal/mindmap` | 扩展 | `visual` 断言类型、基线审批、知识注入设计提示词 |
| `backend/internal/codeintel` | 扩展 | `version_change`、事实人工修正、质量聚合 |
| `backend/internal/delivery` | 扩展 | 报告 revision/快照/stale 联动；知识审核队列 provider |
| `backend/internal/server` | 扩展 | 限流中间件、安全头、新路由装配 |
| `frontend/src/features/execution/chain-*` | 重构 | React Flow 画布、tokens、节点/边、Sheet、图例、MiniMap、follow |
| `frontend/src/features/shell` | 扩展 | 版本切换器、搜索、待办、移动导航 |
| `frontend/src/features/versions` | 扩展 | 脑图 Tab、代码变更、报告 revision、知识入口 |
| `frontend/src/features/agent-center`（或复用 agent-console） | 扩展 | 13 角色目录、只读模式、质量指标 |
| `frontend/src/features/quality` | 新增 | 评测看板（admin） |
| `frontend/src/features/knowledge` | 新增 | 知识列表/审核 |
| `executor/src` | 扩展 | `visual.compare`、pixelmatch、工具调用上限、WS 白名单 |
| `scripts/eval.ps1`、`scripts/slo.ps1`、`scripts/backup.ps1`、`restore.ps1`、`security-audit.ps1` | 新增 | 质量/非功能验收 |

### 3.2 依赖方向

- 继续遵守 `Agent.md` §6.2：上下文之间只经应用端口/适配器；`search`/`workspace` 只依赖各上下文暴露的只读 Provider 端口，不导入他域 domain/infrastructure。
- `quality` 只读 `eval_run`/`eval_metric`，不反向依赖业务上下文；评测脚本通过 HTTP API 驱动。
- 前端：`execution` feature 不反向依赖 `versions` 内部组件；共享类型放 feature 内 `types.ts`；跨 feature 导航走既有 `navigation`/回调。

### 3.3 端口清单（跨上下文）

| 端口 | 提供方 | 消费方 | 用途 |
|---|---|---|---|
| `SearchProvider`（versions/requirements） | `version` | `search` | 版本/需求搜索 |
| `ChainProvider`（requirements） | `version` | `chain` | 链路需求层 |
| `ChainProvider`（cases） | `mindmap` | `chain` | 链路用例层 |
| `ChainProvider`（results） | `execution` | `chain` | 链路结果层 |
| `ChainProvider`（issues） | `delivery` | `chain` | 链路问题层 |
| `ChainProvider`（impact） | `codeintel` | `chain` | 受影响用例 |
| `SearchProvider`（cases） | `mindmap` | `search` | 用例搜索 |
| `SearchProvider`（issues） | `delivery` | `search` | 问题搜索 |
| `TodoProvider`（review） | `version` + `mindmap` | `workspace` | 待评审 |
| `TodoProvider`（takeover/gate） | `execution` | `workspace` | 待接管/待升级 |
| `KnowledgeProvider` | `version` | `mindmap`/`agent` 提示词注入、`workspace` 待办 | 批准知识条目读取 |
| `QualityMetricsProvider` | `quality` | `agent-center` | 最新评测指标 |

---

## 4. 数据模型

### 4.1 迁移清单（只追加，顺序写入 `order.txt`）

| 迁移 | 内容 |
|---|---|
| `codeintel_0006_version_change.sql` | `version_change` 批次代码变更快照 |
| `codeintel_0007_fact_review.sql` | `code_fact_review` 人工事实修正 |
| `version_0006_knowledge.sql` | `knowledge_entry` 版本内知识 |
| `mindmap_0003_visual.sql` | `case_assertion` 放开 `visual` kind + `case_visual_assertion` |
| `execution_0003_visual_evidence.sql` | `assertion_result` 放开 `visual` kind；`evidence.kind` 放宽 `visual_baseline`/`visual_actual`/`visual_diff` |
| `execution_0004_evidence_purge.sql` | `evidence.purged_at/purged_reason`（GC 元数据保留） |
| `quality_0001_eval.sql` | `eval_run` + `eval_metric` |

> 规则：迁移幂等、只追加、不改历史；启动自动执行；`scripts/acceptance.ps1` 从 `order.txt` 动态推导条数。

### 4.2 DDL 摘要

```sql
-- codeintel_0006_version_change.sql
CREATE TABLE version_change (
  id           BIGINT PRIMARY KEY,
  version_id   BIGINT NOT NULL,
  batch_id     BIGINT NOT NULL,
  base_commit  TEXT NOT NULL,
  head_commit  TEXT NOT NULL,
  history_rewritten BOOLEAN NOT NULL DEFAULT FALSE,
  changed_files INTEGER NOT NULL DEFAULT 0,
  impacted_cases INTEGER NOT NULL DEFAULT 0,
  unmapped_count INTEGER NOT NULL DEFAULT 0,
  items        JSONB NOT NULL DEFAULT '[]'::jsonb,
  computed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (version_id, batch_id)
);

-- codeintel_0007_fact_review.sql
CREATE TABLE code_fact_review (
  id           BIGINT PRIMARY KEY,
  repo_id      BIGINT NOT NULL,
  fact_id      TEXT NOT NULL,
  commit_sha   TEXT NOT NULL,
  verdict      TEXT NOT NULL CHECK (verdict IN ('correct','incorrect')),
  reason       TEXT NOT NULL DEFAULT '',
  created_by   BIGINT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_id, fact_id, commit_sha)
);

-- version_0006_knowledge.sql
CREATE TABLE knowledge_entry (
  id           BIGINT PRIMARY KEY,
  version_id   BIGINT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('pattern','pitfall','decision','case_idea')),
  title        TEXT NOT NULL,
  body         TEXT NOT NULL,
  tags         JSONB NOT NULL DEFAULT '[]'::jsonb,
  source_refs  JSONB NOT NULL DEFAULT '[]'::jsonb,
  status       TEXT NOT NULL CHECK (status IN ('draft','approved','rejected')),
  confidence   TEXT NOT NULL DEFAULT 'medium' CHECK (confidence IN ('high','medium','low')),
  created_by   BIGINT NOT NULL,
  reviewed_by  BIGINT,
  review_note  TEXT NOT NULL DEFAULT '',
  revision     INTEGER NOT NULL DEFAULT 1,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- mindmap_0003_visual.sql（节选；约束名按现有实际命名）
ALTER TABLE case_assertion DROP CONSTRAINT IF EXISTS case_assertion_kind_check;
ALTER TABLE case_assertion ADD CONSTRAINT ck_case_assertion_kind
  CHECK (kind IN ('ui','api','db','text','visual'));
CREATE TABLE case_visual_assertion (
  assertion_id        BIGINT PRIMARY KEY,
  selector            TEXT NOT NULL DEFAULT '',
  region              JSONB NOT NULL DEFAULT '{}'::jsonb,
  threshold           NUMERIC(6,5) NOT NULL DEFAULT 0.005,
  baseline_evidence_id BIGINT,
  viewport_width      INTEGER,
  viewport_height     INTEGER,
  device_scale_factor NUMERIC(4,2),
  approved_by         BIGINT,
  approved_at         TIMESTAMPTZ,
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- execution_0003_visual_evidence.sql（节选）
ALTER TABLE assertion_result DROP CONSTRAINT IF EXISTS assertion_result_kind_check;
ALTER TABLE assertion_result ADD CONSTRAINT ck_assertion_result_kind
  CHECK (kind IN ('ui','api','db','text','visual'));
ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;
ALTER TABLE evidence DROP CONSTRAINT IF EXISTS ck_evidence_kind;
ALTER TABLE evidence ADD CONSTRAINT ck_evidence_kind CHECK (kind IN
  ('screenshot','http_exchange','console_log','network_log','download','db_result',
   'visual_baseline','visual_actual','visual_diff'));

-- execution_0004_evidence_purge.sql（节选）
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS purged_at TIMESTAMPTZ;
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS purged_reason TEXT NOT NULL DEFAULT '';

-- quality_0001_eval.sql
CREATE TABLE eval_run (
  id           BIGINT PRIMARY KEY,
  mode         TEXT NOT NULL CHECK (mode IN ('deterministic','real_llm')),
  corpus_version TEXT NOT NULL,
  model        TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL CHECK (status IN ('running','passed','failed','error')),
  summary      JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at  TIMESTAMPTZ
);
CREATE TABLE eval_metric (
  run_id       BIGINT NOT NULL,
  name         TEXT NOT NULL,
  value        NUMERIC(8,5) NOT NULL,
  threshold    NUMERIC(8,5) NOT NULL,
  passed       BOOLEAN NOT NULL,
  detail       JSONB NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY (run_id, name)
);
```

> `version_change.items` 结构：`[{file, changeKind, subjects:[{kind:'route'|'api'|'component', name}], caseIds:[...]}]`；只存摘要，不存代码正文。
> `knowledge_entry.body` 上限 16 KiB；`source_refs` 只存 ID/类型，不存敏感内容。

### 4.3 ID/存储/命名硬约束

- 所有新实体 ID 为雪花十进制字符串（JSON/URL/事件），前端不得 `Number()`；`repo_id`/`created_by`/`reviewed_by` 保持 number（沿用 P2/P4 规则）。
- 文件类：视觉基线/差异图落 `EZTEST_EVIDENCE_DIR` 的证据存储，DB 只存相对路径 + sha256 + size；不暴露文件系统路径。
- 新事件 detail 只放 ID/状态/计数/置信度/降级标记，不放 prompt、凭据、证据正文、报告正文、知识正文。
- 迁移命名 `<context>_NNNN_<name>.sql`；`order.txt` 末尾追加。

---

## 5. 核心设计：探索链路 v2

### 5.1 读模型 API

`GET /api/versions/{versionId}/exploration-chain`

查询参数：

| 参数 | 类型 | 默认 | 约束 | 语义 |
|---|---|---|---|---|
| `batchId` | string | 最近批次 | 必须属于该版本 | 结果/问题层批次；`all` 表示全部批次 |
| `requirementId` | string | 空 | 单需求 | 只看某需求组 |
| `resultStatus` | string | 空 | passed/failed/blocked/inconclusive/skipped/interrupted | 结果状态 |
| `abnormalOnly` | bool | false | — | 仅异常（failed/blocked/inconclusive/interrupted） |
| `issueStatus` | string | 空 | open/confirmed/dismissed | 问题状态 |
| `issueSeverity` | string | 空 | blocker/major/minor/info | 问题严重级 |
| `q` | string | 空 | ≤100 字符 | 需求/用例标题模糊搜索 |
| `includeSupplement` | bool | true | — | 是否包含 AI 补充用例 |
| `revalidationOnly` | bool | false | — | 仅需重验用例 |
| `limit` | int | 300 | 50–2000 | 总节点上限 |
| `requirementLimit` | int | 100 | 1–500 | 需求根上限 |

响应（`{"data": {...}}`）：

```json
{
  "versionId": "1844674407370955161",
  "batch": {"id": "184...", "batchNo": 2, "kind": "incremental", "status": "done"},
  "nodes": [
    {"id": "requirement:184...", "kind": "requirement", "title": "登录", "status": "clarified",
     "priority": "must", "uncovered": false, "caseCount": 3, "executedCount": 3, "failedCount": 0,
     "openQuestionCount": 0},
    {"id": "case:184...", "kind": "case", "title": "登录成功", "status": "executed",
     "source": "user_input", "revalidation": false, "priority": "P1",
     "requirementIds": ["184..."], "stepCount": 3, "assertionCount": 2, "impacted": false,
     "impactReason": ""},
    {"id": "result:184...", "kind": "result", "title": "登录成功", "status": "passed",
     "caseRunId": "184...", "caseNodeId": "184...", "attempt": 2, "durationMs": 2100,
     "failureClass": "", "evidenceCount": 2, "batchId": "184...", "summary": "登录成功"},
    {"id": "issue:184...", "kind": "issue", "title": "验证码错误提示缺失", "severity": "major",
     "status": "open", "category": "defect", "confidence": "high", "caseRunId": "184...",
     "caseNodeId": "184...", "evidenceCount": 1, "codeRefCount": 2}
  ],
  "edges": [
    {"id": "requirement_case:184...:184...", "source": "requirement:184...", "target": "case:184...", "kind": "requirement_case"},
    {"id": "case_result:184...:184...", "source": "case:184...", "target": "result:184...", "kind": "case_result"},
    {"id": "result_issue:184...:184...", "source": "result:184...", "target": "issue:184...", "kind": "result_issue"},
    {"id": "case_issue:184...:184...", "source": "case:184...", "target": "issue:184...", "kind": "case_issue"},
    {"id": "impact_case:...", "source": "requirement:184...", "target": "case:184...", "kind": "impact_case"}
  ],
  "counts": {
    "requirements": 4, "cases": 6, "results": 6, "issues": 2,
    "byResultStatus": {"passed": 5, "failed": 1, "blocked": 0, "inconclusive": 0, "skipped": 0, "interrupted": 0},
    "byIssueSeverity": {"blocker": 0, "major": 1, "minor": 1, "info": 0},
    "uncoveredRequirements": 1, "revalidationCases": 1, "supplementCases": 2
  },
  "truncated": {"requirements": false, "cases": false, "results": false, "issues": false},
  "generatedAt": "2026-10-29T10:00:00Z"
}
```

规则：
- 节点按层稳定排序（需求 `sortOrder,id`；用例 `sortOrder,id`；结果 `attempt desc,id`；问题 `severity rank,created_at desc`）。
- 每个 case 只返回**最新 attempt** 的结果；无结果不生成 result 节点（画布显示无结果态）。
- issue 关联优先级：`caseRunId` 命中当前最新结果 → `result_issue`；否则 `case_issue`；无法关联 → `unlinkedIssues`（不画边，抽屉列表展示）。
- 需求→用例：`coverage.matrix` 与 `case_source(refKind=requirement)` 并集；AI 补充用例带 `source=ai_supplement`；`needs_revalidation` 带 `revalidation=true`。
- 影响用例：读 P4 最新 analysis 的候选（`selected=true` 或 high/medium confidence），生成 `impact_case` 边；无分析时省略。
- 覆盖缺口：需求 `uncovered=true` 当且仅当无关联用例；`counts.uncoveredRequirements` 来自 coverage。
- 权限：登录用户；版本不存在 404；**本端点**非法参数 400 `invalid_argument` + `details{field,reason}`（既有端点状态码不变，见 C6）；批次不属于版本 404。
- 缓存：响应 `Cache-Control: no-store`；服务端不做跨请求缓存（数据实时性优先），单请求查询预算见 §5.2。

### 5.2 服务端聚合与查询预算

单次请求最多 10 条 SQL（集成测试以查询计数器断言）：
1. version + batch（1）
2. requirements（1）
3. cases（1）
4. case_sources（1）
5. steps/assertions 计数聚合（1）
6. 每 case 最新 result（`DISTINCT ON (case_node_id)`，1）
7. evidence 计数（1）
8. issues + issue_refs（1–2）
9. coverage（1）
10. P4 impact candidates（1，可空）

约束：
- 不得逐行调用 `CountOpenQuestions`/`refsFor`/`loadAnchorIndex`；改为聚合/批量。
- `limit` 截断时按层设置 `truncated` 标志并返回 `counts` 真实总数（counts 用聚合查询，不随截断丢失）。
- 结果层必须取全（不受 20 默认限制）；若总数超过 `limit`，`truncated.results=true` 且前端显示“已显示前 N 条，请收窄筛选”。
- 单请求 P95 ≤ 800ms（本地 200 节点）；超过则记录慢查询日志。

### 5.3 前端架构（React Flow + 保留资产）

目录（最终形态）：

```text
frontend/src/features/execution/
├─ chain/
│  ├─ chain-tokens.ts          # 节点/边/状态/严重级唯一色板（含 dark/reduced-motion）
│  ├─ chain-types.ts           # 读模型 DTO 类型
│  ├─ use-exploration-chain.ts # REST + SSE 刷新 + 错误/重试
│  ├─ chain-layout.ts          # 保留：确定性四层布局（输入改为读模型节点）
│  ├─ ChainCanvas.tsx          # React Flow 容器（懒加载）
│  ├─ nodes/RequirementNode.tsx
│  ├─ nodes/CaseNode.tsx
│  ├─ nodes/ResultNode.tsx
│  ├─ nodes/IssueNode.tsx
│  ├─ edges/ChainEdge.tsx      # 语义色 + 标签 + 箭头 + 动画
│  ├─ ChainLegend.tsx
│  ├─ ChainToolbar.tsx         # 筛选/搜索/跟随/重置
│  ├─ ChainMinimap.tsx         # 复用 React Flow MiniMap + 类型着色
│  ├─ ChainSheet.tsx           # 右侧覆盖 Sheet（Esc/焦点归还）
│  └─ ChainStates.tsx          # 骨架/空/错误/降级/截断/重连
├─ components/ExplorationTab.tsx # 组合层：数据/筛选/Sheet/问题列表
└─ hooks/useExecution.ts         # 保留，结果层改用读模型
```

关键约束：
- `ChainCanvas` 用 `next/dynamic` + `ssr:false` 懒加载，避免首屏包体与 SSR 问题。
- `chain-layout.ts` 及其移动后的测试文件（`chain/chain-layout.test.ts`）保持纯函数；布局输入改为读模型节点，坐标语义不变。
- 节点坐标由 `chain-layout.ts` 产出；React Flow 节点 `position` 固定，不允许拖拽改布局（保持确定性）。
- 禁用 React Flow 自带键盘导航（`disableKeyboardA11y`），保留自有 `role=tree`/roving focus/方向键/Home/End/Enter/Esc；节点容器补 `aria-level/setsize/posinset/label`。
- 沿用既有 `data-testid` 命名；新增 `chain-node-<kind>`、`chain-edge-<kind>`、`chain-minimap`、`chain-sheet`、`chain-truncated`、`chain-stream-status`。
- 主题色从 `chain-tokens.ts` 读取，禁止组件内硬编码 hex；状态色与全站 `StatusBadge` 对齐。
- `prefers-reduced-motion` 下关闭脉冲/流动动画，保留静态高亮。

### 5.4 视觉语法与 ARTEX 对齐

| 元素 | 目标（ARTEX 对齐点） | EzTest 特有 |
|---|---|---|
| 画布 | 点阵背景、全宽 72vh、fitView、`minZoom 0.1` | 保留筛选条与流状态 |
| 节点卡 | 宽 232–240px、圆角 2xl、图标色块+类型名、标题 2 行、状态徽章、右下 `#id` | 4 类节点语义、AI 补充虚线+角标、needs_revalidation 琥珀环、结果耗时/证据数、问题严重级/确认状态 |
| 边 | 语义色（需求→用例靛、用例→结果状态色、结果→问题红、影响琥珀虚线）+ 标签 + 箭头；目标运行中 animated | 保持既有边语义，新增 `impact_case` |
| 图例 | 左上 overlay 卡片：节点类型 + 边关系；空态提示 | 增加“仅异常/截断”说明 |
| MiniMap | 右下、按节点类型着色、可平移缩放 | 点击定位需求组 |
| 抽屉 | 右侧覆盖 Sheet（max-w-md），不挤压画布 | 摘要/证据/代码位置/深链/问题列表 |
| 运行反馈 | running 节点蓝色脉冲、入边流动 | 跟随执行（轻量） |
| 工具栏 | 缩放/fit/重置 | 需求/状态/仅异常/问题严重级/搜索/AI 补充/重验/跟随 |

> 不照搬：8 类节点/4 类边、20s 轮询、2000/5000 全量、raw JSON 抽屉、不可逆 digest 折叠、G6 力导向、主题 preset。

### 5.5 交互与状态

- **筛选**：需求、结果状态、仅异常、问题严重级、问题状态、AI 补充、需重验、标题搜索；服务端筛选（`q` 防抖 300ms），保留 URL 查询参数（`?tab=exploration&req=&status=&abnormal=&q=`）。
- **折叠**：按需求分组折叠；显示“折叠 N 项”；>200 自动折叠；focus 模式只显示选中需求组 + 直接邻居。
- **跟随执行**：开关开启时，当 running 结果出现，自动 `setCenter` 到该节点；用户手动 pan/zoom 后暂停跟随，显示“恢复跟随”按钮。
- **Sheet**：节点点击打开；Esc/遮罩/X 关闭；打开时焦点进入 Sheet，关闭归还触发节点；问题节点提供“查看问题/证据/代码位置”；结果节点提供“打开执行结果/证据”；用例节点提供“打开脑图用例”；需求节点提供“查看需求”。
- **实时**：`version:{id}` SSE；`case.*`/`run.*`/`batch.*`/`issue.*`/`report.*`/`code.changed` 触发 500ms 合并刷新；请求 abort 旧请求；重连显示“重连中”，不显示空态；刷新失败保留旧数据 + 错误条。
- **截断**：任一 `truncated` 为 true 时显示琥珀横幅“已显示前 N 条（共 M 条），请收窄筛选”；不显示“0 个问题”假象。
- **空/错**：骨架屏；无数据独立空态；分图层错误命名（需求/用例/结果/问题）；问题层失败显示“问题数据不可用”而非 0；全失败红条 + 重试。
- **列表视图降级**：提供“列表视图”切换（表格：类型/标题/状态/关联/证据），键盘与读屏用户可用；不作为默认。
- **移动端**：<1024 时 Sheet 全宽、筛选折叠进抽屉；画布高度 60vh；触控 pinch 由 React Flow 提供；工具栏按钮 ≥44px。

### 5.6 正确性修复清单（P0，先于视觉）

| # | 缺陷 | 修复 |
|---|---|---|
| C1 | 结果层默认 20 条且忽略 `hasMore` | 链路读模型一次性返回 latest per case；`truncated` 显式 |
| C2 | case-run 列表 evidence 恒空 | 链读模型返回 `evidenceCount`；Sheet 打开时按 `caseRunId` 取详情/证据 |
| C3 | graph/requirements 不订阅 SSE | `use-exploration-chain` 订阅版本流并 500ms 合并刷新；保留手动刷新 |
| C4 | 深链名不副实 | `?tab=exploration&node=` 打开 Sheet；`?tab=requirements&requirement=`；`?tab=mindmap&case=`；`?tab=results&caseRun=`；`evidence` 参数打开查看器 |
| C5 | 100 条静默截断 | 服务端 counts + `truncated` + 横幅；`limit` 上限 1000；筛选可收窄 |
| C6 | 错误文案误导 | `limit` 非整数 → `invalid_argument` “limit must be an integer”；超范围 → “limit must be between 1 and N” + `details{field,min,max}`；不改变现有 400/422 状态码 |
| C7 | 前后端 limit 常量漂移 | 前端只使用读模型端点；保留 `limit ≤ 100` 单测；后端补 100/101/200 边界测试 |
| C8 | N+1 读放大 | 读模型批量查询；旧端点不动；新增查询预算集成测试 |
| C9 | 证据按钮空 | 读模型 `evidenceCount` >0 才显示；点击懒加载详情 |
| C10 | `attempt` 不可见 | 结果节点显示 `attempt`；Sheet 显示批次/尝试/失败分类 |

### 5.7 深链与可访问性

- 统一 URL 状态：`?tab=exploration&node=<kind>:<id>&batch=<id>&req=<id>&status=<s>&abnormal=1&q=...`；刷新/分享可复现。
- `node` 打开 Sheet；非法/已删除节点显示“节点不存在/已刷新”提示，不崩。
- 键盘：↑↓ 同层移动；←→ 折叠/展开或父/子；Home/End；Enter/Space 打开 Sheet；Esc 关闭；`aria-expanded`/`aria-selected`/`aria-posinset`/`aria-setsize`。
- 图表不只靠颜色：图标 + 文本 + 形状；对比度 ≥ 4.5:1（正文）/3:1（大字）。
- `prefers-reduced-motion`；焦点环可见；Sheet 焦点陷阱与归还。

### 5.8 性能与规模

- 目标：200 节点/400 边 P95 可交互 ≤ 2.5s；500 节点内 pan/zoom 不卡顿；超过 500 节点提示收窄筛选（`EZTEST_CHAIN_MAX_NODES` 默认 300，可按部署调整）。
- `onlyRenderVisibleElements` + 节点 `memo`；`chain-layout` 纯函数结果缓存（结构签名）；位置保留避免刷新跳动。
- 懒加载 React Flow 包；首屏不引入。
- 压测脚本纳入 `scripts/slo.ps1`；记录 measurements。

### 5.9 测试与验收

- 单测：`chain-layout`（保留 + 适配读模型）、`chain-tokens`、节点渲染（状态/图标/徽章）、筛选映射、截断/错误/空态。
- 组件：`ChainCanvas` 渲染 200 节点、键盘导航、Sheet 焦点、MiniMap/follow（mock React Flow 尺寸；`ResizeObserver`/`DOMMatrix` polyfill）。
- E2E：真实后端打开链路、无错误条、问题节点/证据 Sheet/深链/筛选/折叠/跟随/键盘；>100 fixture 截断横幅。
- 视觉：`docs/acceptance/p8/wave1/*.png` 与 ARTEX 参考对照；人工走查清单。
- 性能：SLO 脚本；200 节点 P95。
- 可访问性：axe 无 serious/critical；键盘全流程。

---

## 6. 全局壳

### 6.1 版本切换器（顶栏）

- 位置：Topbar 左侧 Logo 之后；展示当前版本名 + 状态徽章；下拉含最近 8 个版本（localStorage `eztest.recentVersions`）+ 搜索框（本地过滤已加载列表，输入时调 `GET /api/versions?q=`）。
- 切换：保留当前 `tab`/`batch`/筛选参数（若目标版本无该批次则回退默认）；写入最近列表；不整页刷新（`router.push`）。
- 空/错：无版本显示“暂无版本”；请求失败显示重试，不阻塞壳。
- 权限：所有登录用户可见（版本读权限现状）。

### 6.2 全局搜索

- 入口：Topbar 搜索框 + `Ctrl/Cmd+K` 命令面板；`/` 聚焦（非输入态）。
- API：`GET /api/search?q=&types=version,requirement,case,issue&limit=`
  - `limit` 每类型默认 5、最大 20；`q` 长度 1–100；空 `q` 返回 400 `invalid_argument`。
  - 响应 `{"data":{"groups":[{"type":"version","items":[{"id","title","subtitle","versionId","versionName","status","url"}]}],"total":N,"truncated":bool}}`。
  - ILIKE 匹配 title/name；按更新时间排序；每类型最多 `limit`；总上限 `types*limit`。
- 权限：登录用户；结果 URL 深链到版本/Tab/对象；不返回凭据/证据正文。
- 前端：300ms 防抖、请求取消、键盘上下选择/Enter 打开/Esc 关闭、无结果空态、错误重试；最近搜索（localStorage，最多 5 条）。
- 实现：`internal/search` 服务 + 3 个 Provider（version/mindmap/delivery）；不跨上下文读表；查询预算每类型 1 条 SQL。

### 6.3 待办中心

- API：`GET /api/todos?limit=50`
  - `review`：`version.status=reviewing` 的版本（标题、待评审时间、版本 ID、深链 `?tab=mindmap`）。
  - `takeover`：`case_run.pending_takeover` waiting（仅 admin 返回；普通用户不返回或只读计数）。
  - `gate`：未解决的 gate（`gate.pending` 对应实体；与 takeover 去重）。
  - 响应 `{"data":{"items":[{"kind","title","versionId","versionName","severity","createdAt","url"}],"counts":{"review":N,"takeover":N,"gate":N}}}`。
- 顶栏铃铛 + 计数徽章；面板分组、深链、空态；30s 轮询 + 路由切换刷新；错误显示重试不影响壳。
- 权限：review 所有登录用户；takeover/gate 仅 admin（普通用户不显示该分组）。
- 实现：`internal/workspace` + Provider（version/execution）；只读。

---

## 7. 版本详情增强

### 7.1 脑图 Tab

- Tab 顺序（10 项）：需求与澄清、用例脑图、总览、执行会话、探索链路、实时播报、结果与证据、问题、报告、代码与变更。
- `?tab=mindmap` 渲染嵌入版 `MindmapPage`（`embedded=true`）：去掉全屏页头/返回按钮，保留画布/评审/导出；高度自适应 Tab 容器。
- `/versions/{id}/mindmap` 全屏路由保留，读取 `?case=` 选中节点；Tab 与全屏共享数据 hook，避免重复请求。
- `?tab=mindmap&case=<nodeId>` 深链；非法 case 显示提示不崩。
- 空白/错误/重连与全屏一致；`data-testid` 保留并新增 `version-tab-mindmap`。

### 7.2 代码与变更完整版

- `version_change` 快照：
  - 仅当版本绑定了代码仓时生成；建批/批完成时计算并 upsert `(version_id,batch_id)`；`items` 含文件、变更实体、映射用例；`history_rewritten` 来自批次 `base_fallback_reason`；无代码仓时 `GET /changes` 返回空态而非报错。
  - `/api/versions/{id}/changes` 优先读快照；缺失时回退读时派生（兼容 P7）；返回批次时间线数组。
  - 发布 `code.changed` 事件（version 通道），detail 只放 batch/head/文件数/影响数。
- 前端「代码与变更」Tab：批次时间线（Batch 1→N，commit/新增需求/影响用例）、变更文件/实体→受影响用例、未映射变更、代码锚点、diff 抽屉；空态“本批无代码变更”；错误重试。
- 事实人工修正：`POST /api/code/repos/{repoId}/facts/{factId}/reviews`（verdict/reason），写入 `code_fact_review`；FactsPanel 显示“已人工修正/错误”标记与原因；同一 fact/commit 唯一，更新覆盖；审计记录。
- 质量聚合：`GET /api/code/repos/{repoId}/quality` 返回证据通过率、无效事实、置信度分布、人工修正数、影响 precision/recall（来自最近评测）。

### 7.3 报告优化

- revision 选择器：报告 Tab 顶部下拉列出当前批次所有 revision（`GET /api/versions/{id}/reports?batchId=` 已含列表；补 `revision` 字段展示）；`?tab=report&report=<id>` 深链。
- D49 快照字段：报告 `content.batch` 增加 `baseFallbackReason`/`historyRewritten`；旧报告读取时缺失则显示“未记录”，不重写历史。
- stale 联动：`input_fingerprint` 纳入每用例证据数/失败分类计数；证据新增或问题变化时读时判定 stale 并发 `report.stale`（只发一次）；重新生成 revision+1。
- 导出的 Markdown 增加“代码基线/回退标记”字段；PDF 留 P9。
- 证据前后对比：见 §8.3。

---

## 8. 管理面与体验硬化

### 8.1 Agent 中心

- 路由 `/agents`：管理员可配置；普通用户只读。
- 13 逻辑角色目录（新增 `GET /api/agent-center/catalog`，登录可读）：

| # | 角色 | profile | 说明 |
|---|---|---|---|
| 1 | 澄清对话 | `clarification-dialog` | P3 |
| 2 | 需求分析 | `requirement-analysis` | P3 |
| 3 | 上下文侦察 | `context-recon` | P3 |
| 4 | 应用探索 | `app-exploration` | P5 |
| 5 | 测试设计 | `case-design` | P5 |
| 6 | 设计评审 | `case-review` | P5 |
| 7 | 数据/前置规划 | `case-data-plan` | P5 |
| 8 | 执行 | `execution-agent` | P6 |
| 9 | 补全 | `supplement-agent` | P7 |
| 10 | 失败分析 | `failure-analysis-agent` | P7 |
| 11 | 问题整理 | `issue-triage-agent` | P7 |
| 12 | 报告整理 | 无（确定性服务） | 不伪造 profile，标注“确定性生成” |
| 13 | 知识沉淀 | 无（人工审核 + P8 版本内） | 标注“人工审核入库” |

- 目录返回：角色名、profile key、enabled、model、prompt version、allowed tools、最近运行数/成功率、最新评测指标（来自 `quality` 最新 run）。
- 运行观察：`GET /api/agent-center/sessions?role=&status=&limit=` 只读；跳转既有会话详情（P1）或只读 transcript；普通用户不显示写操作。
- 配置：沿用 P1 `PATCH /api/agents/profiles/{id}`（admin-only）；普通用户无编辑按钮。
- 质量指标：全局评测指标 + 每角色运行次数/失败率；空态“尚未运行评测”。

### 8.2 设置完善与审计查看器

- 设置页 Tab：用户、LLM、基础配置、审计日志（admin）。
- 基础配置：只读展示环境/版本/端口/限流阈值/GC 保留期/SLO 阈值/证据目录（不展示密钥）；提供“重新加载配置”说明（需重启）。
- 审计日志查看器：`GET /api/admin/audit` 既有；前端筛选（actor/action/from/to）、分页、详情、CSV/Markdown 导出（前端生成）；只读。
- 审计覆盖补齐：resource/Session/版本/脑图写操作写入 `audit_logs`（新增 action；不影响既有事件投影）；秘密字段永不入审计。
- 权限：设置与审计仅 admin；普通用户只保留主题。

### 8.3 移动端、可访问性、空/错/重连、证据对比

- 移动导航：<1024 顶栏汉堡；侧边栏抽屉（overlay + 焦点陷阱 + Esc + 遮罩关闭 + body scroll lock）；路由切换自动关闭；375/768/1280 三档可导航。
- 可访问性：`axe-core` 作为 vitest dev 依赖；关键页面（登录、版本列表、版本详情 4 Tab、设置、Agent 中心）无 serious/critical；跳过导航链接；焦点可见；图表文本替代；`prefers-reduced-motion`。
- 空/错/重连：新页面统一 `EmptyState`/`ErrorState`/`Skeleton`；SSE 断开显示“重连中”，旧数据保留；重试按钮；错误含 request id。
- 证据前后对比：EvidenceViewer 支持 `compareWith` 参数；并排/滑块对比截图；差异用高亮边框；深链 `?evidence=<id>&compare=<id>`；不做像素级 diff（归视觉断言）。
- Playwright 增加 375×667 与 768×1024 视口冒烟（导航/关键页可达）。

---

## 9. 质量平台

### 9.1 评测集与指标

**语料**（`eval/`）：
- `eval/corpus/`：固定 fixture Git 仓（脚本生成，含可预期 diff）、需求 JSON、环境/账号配置、用例真值。
- `eval/ground-truth/`：受影响用例、失败分类、问题真值、补充缺口、设计覆盖。
- `eval/baseline.json`：上次通过的指标基线（提交入库）。
- `eval/README.md`：语料维护/更新流程。

**Runner**：`scripts/eval.ps1`
- 默认确定性模式：启动 API（独立 `eztest_eval` 库）+ fake LLM + fixture executor；按阶段跑澄清/设计/评审/执行/补全/失败分析/问题/报告；收集 API 数据计算指标。
- `-RealLLM`（gated，`EZTEST_RUN_REAL_LLM=1`）：真实 DeepSeek + 真实 executor（若可用），产出报告，不作硬门禁。
- 结果只经 admin Bearer API 写入 `quality.eval_run`/`eval_metric`（`POST /api/quality/eval-runs`），不直接写业务表；同时输出 `.tmp/p8-eval/*.json`。
- 与 `scripts/acceptance.ps1` 集成：`-StaticOnly` 检查语料/基线存在；完整验收执行 `eval.ps1`；失败则整体失败。

**指标与阈值（确定性门禁）**：

| 指标 | 定义 | 阈值 |
|---|---|---|
| 设计覆盖率 | 已覆盖已确认需求 / 已确认需求（coverage matrix） | ≥ 0.90 |
| 影响命中率 | 命中 ground-truth 受影响用例 / ground-truth 总数（recall；另报 precision） | recall ≥ 0.90，precision ≥ 0.80 |
| 失败分类准确率 | 分类正确用例 / 注入失败用例 | = 1.00 |
| 问题误报率 | 无真值缺陷的 AI 问题 / AI 问题总数 | ≤ 0.05 |
| 补充命中率 | 命中 ground-truth 缺口的补充用例 / 补充用例 | ≥ 0.80 |

- 回归规则：任一指标不得比 `baseline.json` 下降超过 0.05；否则失败。
- 真实 LLM 报告阈值（仅报告，不门禁）：设计覆盖 ≥0.80、影响 recall ≥0.70、分类准确 ≥0.80、问题误报 ≤0.15、补充命中 ≥0.50；低于阈值记录为“质量观察”，不阻断发布但必须在验收记录中说明。
- 评测看板 `/quality`（admin）：最近 run、指标、阈值、通过状态、历史趋势（简单表格）；普通用户不可见。

### 9.2 代码事实质量与影响度量

- API `GET /api/code/repos/{repoId}/quality`：证据通过率、无效事实数、置信度分布、人工修正数、最近影响分析 precision/recall（来自评测 ground truth）、缓存命中率；只读、登录可读。
- 人工修正：`code_fact_review` + API + UI（§7.2）；修正不影响历史事实版本，只标注意见并在质量指标计数。
- 评测 ground truth：`eval/ground-truth/impact.json` 固定 commit 对的受影响用例；评测脚本比对 P4 候选（selected/置信度）计算 precision/recall。
- 前端：代码仓页新增“质量”区块；事实面板显示修正状态。

### 9.3 视觉断言（D7）

**模型**：
- 新断言 `kind=visual`；`case_visual_assertion` 存 `selector/region/threshold/baseline_evidence_id/viewport/DPR`。
- 编辑：`PUT /api/versions/{id}/mindmap/assertions/{assertionId}/visual`（版本可编辑用户；`nodeRevision` OCC 沿用）；`selector` 为空表示全页；`region` 为 `{x,y,width,height}` 归一化坐标；`threshold` 0–0.1。
- 基线审批：`POST /api/versions/{id}/mindmap/assertions/{assertionId}/visual/baseline`（admin）：body `{evidenceId}`；校验 evidence 属于该版本/用例、PNG、尺寸与 viewport 匹配；写 `baseline_evidence_id/approved_by/approved_at`；发 `case.visual_baseline_approved` 事件。
- 基线生成：失败/通过运行不自动成为基线；UI 可从本次截图“提交为基线候选”，admin 审批后生效。

**执行**：
- executor 新调用 `visual.compare`：API 只传基线副本（base64 PNG）与 `selector/region/threshold/viewport/DPR`；**实际截图由 executor 在目标页完成**，API 不传 actual 图。
- executor 用 `pixelmatch` + `pngjs`（MIT）比对；返回 `{diffRatio, diffPng(base64), width,height, mismatchBoxes}`；大小上限 `EZTEST_VISUAL_BASELINE_MAX_BYTES`（默认 5 MiB）。
- 结果：
  - 无基线 → `inconclusive(case, missing_visual_baseline)`。
  - viewport/DPR/UA 指纹与基线不一致 → `inconclusive(environment, visual_environment_mismatch)`。
  - `diffRatio > threshold` → `failed(application, visual_mismatch)`。
  - 否则 passed。
- 证据：`visual_baseline`（基线引用）、`visual_actual`、`visual_diff` 落证据存储；sha256/大小；不暴露路径。
- 前端：案例编辑器支持 visual 断言、阈值/区域编辑；基线审批入口（admin）；证据查看器展示 actual/diff/并排；报告/评测纳入 visual 场景。
- 降级：executor 不支持 `visual.compare` → `inconclusive(case, unsupported_assertion_kind)`，不假通过。

### 9.4 知识沉淀（D4，版本内）

- 表/API/UI 见 §4/§11；状态 `draft → approved/rejected`；revision CAS；驳回必填原因。
- 创建：任何登录用户可在版本内创建草稿（title/body/kind/tags/source_refs）；`source_refs` 可指向需求/用例/问题/证据 ID。
- 审核：admin；`GET /api/versions/{id}/knowledge?status=draft` 队列；批准后 `GET /api/knowledge` 全局只读（批准条目，按版本筛选）。
- 注入：P3 澄清与 P5 设计运行前，读取本版本已批准条目（最多 `EZTEST_KNOWLEDGE_MAX_INJECT=5` 条、总量 ≤4 KiB），拼入 system prompt 的“版本知识”段；运行结果记录 `knowledgeRefs`（ID 列表）以便追溯；预算不足时截断并记录 warning；不注入 rejected/draft。
- 待办：draft 条目进入待办中心 `review` 分组（admin）。
- UI：`/knowledge` 页（列表/筛选/详情/版本深链）；版本详情总览入口 + 侧栏“知识”入口；审核抽屉。
- P9：跨版本检索/复用/知识库管理。

---

## 10. 非功能硬化

### 10.1 限流与 SLO

- 中间件 `internal/server/ratelimit`：用户 ID（认证后）/IP（认证前）令牌桶；配置 `EZTEST_RATE_LIMIT_RPS`（默认 50，0=关闭）、`EZTEST_RATE_LIMIT_BURST`（默认 100）、`EZTEST_RATE_LIMIT_SSE_MAX`（默认 10 并发/用户）。
- 行为：超限 429 `rate_limited` + `Retry-After` + `X-Request-ID`；健康检查、静态资源、SSE 心跳不计入普通桶；SSE 超并发返回 429 并关闭旧流提示。
- 审计/日志：限流命中计数日志（不含用户敏感信息）；管理端基础配置页只读展示阈值。
- SLO 脚本 `scripts/slo.ps1`：
  - 数据集：100 版本/50 需求/200 用例/400 边/10k 事件。
  - 测量：版总览首屏 API+浏览器 navigation timing P95；链路端点 P95；8 并发批次执行（fixture executor）成功率；代码变更集 API P95；代码分析非模型开销。
  - 阈值见 §1.5；输出 `docs/acceptance/p8/slo.md` + `.tmp/p8-slo/metrics.json`；超阈值失败。
  - 本地环境口径，不作为生产 SLA 承诺；生产需按部署环境复测。

### 10.2 GC/保留

- 配置：`EZTEST_GC_RETENTION_DAYS`（默认 30）、`EZTEST_GC_BATCH_KEEP`（默认 3，最近批保护）、`EZTEST_GC_ENABLED`（默认 false）。
- 对象：证据文件、附件、导出、报告文件、孤儿文件（无 DB 行）。**P8 实施注记（Task 28 fix round 1 re-scope）**：实际自动清理仅覆盖 `EZTEST_EVIDENCE_DIR` 下的执行证据文件；附件、导出、报告文件与无 DB 行的孤儿文件缺少安全的引用/保护判定，**不在 P8 自动删除范围**，明确留给 P9/后续（`docs/operations.md`“已知限制”与设置页文案同步）。
- 保护集：最近 N 批证据；未关闭（open/confirmed）问题引用；基线证据；visual 基线；当前批次运行中；已清除（`purged_at` 非空）行。**P8 实施注记（fix round 1）**：`report_ref(ref_kind='evidence')` 无生产写入——报告快照只含证据计数，不引用 evidence id——因此发布/过期（stale）报告不是有效的证据保护类，首版的空 claim 已从 SQL/UI/文档移除；`evidence_ref(ref_kind='report'|'issue')` 同样无写入者。若未来引入报告→证据引用，必须恢复保护 SQL 并补“报告服务生成 → GC 保护”的真实 PostgreSQL 集成测试。
- 行为：`POST /api/admin/gc`（admin）body `{dryRun:true}` 默认 dry-run；返回候选数/字节/保护数/单次候选上限（10000）；非 dry-run 删除文件并标记 DB（evidence `purged_at`/`purged_reason`，保留元数据）；写审计；UI 设置页展示 dry-run 结果与执行按钮（二次确认）。
- 定时：单实例维护任务每日一次（leader 持有），默认只 dry-run 记录；生产需显式开启。
- 安全：绝不删除受保护对象；删除前校验 sha256/引用；缺失/损坏文件不删除不标记（缺失按跳过，损坏按跳过）；失败回滚标记不删文件；**标记后的删除/回滚使用脱离请求取消的有界后台 context（`context.WithoutCancel` + 30s）**，保证 mark 后必然 remove 或 rollback；`purged_reason` 保留 Unicode、控制字符转义、最多 120 字符；审计 best-effort；日志不含内容。

### 10.3 备份恢复

- `scripts/backup.ps1`：`pg_dump -Fc` + 证据/报告/导出/附件/对象目录 tar + `manifest.json`（文件清单/sha256/大小/配置键名）+ 版本/commit；输出到目标目录；不打包 `.env`/密钥。
- `scripts/restore.ps1`：目标必须为空库/空目录；校验 manifest；恢复 DB + 目录；逐文件 sha256 校验；输出校验报告；拒绝覆盖非空目标（除非 `-Force` 且二次确认）。
- 演练：本机执行备份→新建库/目录→恢复→对比行数/哈希/关键页面；记录 RPO（按需备份，无 PITR）与 RTO（目标 ≤30 分钟）到 `docs/acceptance/p8/backup-drill.md`。
- 文档：`docs/operations.md` 写备份/恢复/密钥丢失说明（`EZTEST_SECRET_KEYS` 丢失则 LLM Key 不可恢复）。

### 10.4 安全审计与审计覆盖

- `scripts/security-audit.ps1`：
  - 秘密扫描（仓库/构建产物，排除 `.env`/`.tools`/`.tmp`）；
  - `govulncheck ./...`（Go 工具存在时；不存在则记录 SKIP 并说明）；
  - `pnpm audit --prod`（frontend/executor）；
  - 权限矩阵检查：普通用户对 admin-only 端点 403、只读端点 200；
  - 依赖许可清单（MIT/Apache-2.0 白名单；新增依赖必须登记）。
- 审计覆盖：resource/Session/版本/脑图写操作补 `audit_logs`（action 命名 `<context>.<verb>`）；审计查看器筛选/导出；秘密/凭据/Session 原文永不入审计。
- 安全头：Next `headers()` 增加 `X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`、`Permissions-Policy`；CSP 先 report-only（避免破坏内联样式），文档记录。
- 已知边界：token 仍在 sessionStorage（P0 决策），无 HttpOnly 会话；TLS/反代由部署方负责；LLM base_url 信任管理员。

### 10.5 多实例单活与执行器硬化

- `internal/platform/leader`：Postgres advisory lock（`pg_try_advisory_lock`）；`EZTEST_INSTANCE_ID`（默认 hostname+pid）、`EZTEST_LEADER_LOCK_KEY`（默认 `1163411512`）。
- 使用点：自主 worker tick、执行调度 tick、GC 维护任务；每次 tick 尝试加锁，完成后释放；未获锁跳过并记录 debug；启动/退出日志含 instance id。
- 兼容：单实例行为不变；集成测试用两个连接模拟两实例，断言同一 tick 只有一个执行。
- 执行器硬化：
  - `EZTEST_EXEC_MAX_TOOL_CALLS` 在调度/执行路径强制计数，超限 `inconclusive(model, tool_call_budget_exhausted)`；
  - WS 连接按 `allowed_hosts` 白名单（浏览器上下文）；
  - executor 后台超时任务收尾：超时后取消 context 并落 `inconclusive(model, executor_timeout)`，不悬挂。
- 配置接线：`EZTEST_ANALYSIS_MAX_CASES`（默认 20）、`EZTEST_SUPPLEMENT_MAX_CASES`（默认 5）真正读取；`EZTEST_AUTONOMY_BATCHES_PER_TICK` 上限钳制文档修正。
- 报告快照：`content.batch` 写入 `baseFallbackReason`/`historyRewritten`（§7.3）。

### 10.6 配置与工程

- CI：`.github/workflows/ci.yml`：Go 单元/集成（服务 PostgreSQL）、前端 lint/typecheck/test、executor test、fake LLM 契约、静态验收；不跑 gated 真实 LLM/E2E（E2E 需浏览器与完整服务，留给本地/定时）。
- `.gitignore` 增加 `.data/`、`backend/.data/`、`.tmp/` 已有则确认；清理未跟踪导出文件；`docs/superpowers/specs/Untitled-1.md` 删除或归档。
- P0 文案清理：顶栏“P0 · 地基版本”、侧栏“P0 地基运行中”、页脚“P0 平台地基”改为 P8/平台版本；导航徽章更新。
- `scripts/acceptance.ps1` 头部注释与检查项更新 P8；旧验收记录标注“历史”。
- README P7 命令“目标命令”表述修正为已通过记录。

---

## 11. API/事件/迁移/环境变量总表

### 11.1 新增/变更 API

| 方法/路径 | 权限 | 说明 |
|---|---|---|
| `GET /api/versions/{id}/exploration-chain` | 登录 | 链路读模型 |
| `GET /api/search` | 登录 | 全局搜索 |
| `GET /api/todos` | 登录（takeover 仅 admin） | 待办聚合 |
| `GET /api/agent-center/catalog` | 登录 | 13 角色目录 + 指标 |
| `GET /api/agent-center/sessions` | 登录 | 运行观察只读 |
| `GET /api/agent-center/quality` | 登录 | 最新评测指标 |
| `GET /api/versions/{id}/knowledge` | 登录 | 版本知识列表 |
| `POST /api/versions/{id}/knowledge` | 登录 | 创建草稿 |
| `PATCH /api/versions/{id}/knowledge/{kid}` | 登录（创建者/admin） | 编辑草稿 |
| `POST /api/versions/{id}/knowledge/{kid}/review` | admin | 批准/驳回 |
| `GET /api/knowledge` | 登录 | 全局已批准只读 |
| `PUT /api/versions/{id}/mindmap/assertions/{aid}/visual` | 登录（编辑） | visual 参数 |
| `POST /api/versions/{id}/mindmap/assertions/{aid}/visual/baseline` | admin | 基线审批 |
| `GET /api/code/repos/{repoId}/quality` | 登录 | 代码质量聚合 |
| `POST /api/code/repos/{repoId}/facts/{factId}/reviews` | 登录 | 事实人工修正 |
| `GET /api/quality/eval-runs`、`GET /api/quality/eval-runs/{id}` | admin | 评测结果 |
| `POST /api/quality/eval-runs` | admin（脚本 token） | 写入评测 run |
| `POST /api/admin/gc` | admin | GC dry-run/执行 |
| `GET /api/admin/audit`（既有） | admin | 增加筛选/导出所需字段 |

> 所有写接口沿用 `Idempotency-Key`；错误沿用 P0 信封；ID 十进制字符串。

### 11.2 新增事件（`version:{id}` 通道）

| 事件 | 触发 | detail |
|---|---|---|
| `code.changed` | 批次创建/完成生成 `version_change` | `{batchId,baseCommit,headCommit,changedFiles,impactedCases,historyRewritten}` |
| `case.visual_baseline_approved` | 基线审批 | `{caseNodeId,assertionId,baselineEvidenceId,approvedBy}` |
| `knowledge.created` | 知识草稿创建 | `{knowledgeId,kind,status}` |
| `knowledge.reviewed` | 批准/驳回 | `{knowledgeId,status,reviewedBy}` |
| `report.stale`（既有扩展） | 证据计数变化 | 既有字段 + `reason` |

> 细节只放 ID/状态/计数；不放知识正文、基线图片、证据正文。

### 11.3 迁移

见 §4.1；`order.txt` 末尾追加；验收脚本动态推导。

### 11.4 新增环境变量

| 变量 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `EZTEST_CHAIN_MAX_NODES` | 300 | 50–2000 | 链路节点总上限 |
| `EZTEST_CHAIN_MAX_REQUIREMENTS` | 100 | 1–500 | 链路需求根上限 |
| `EZTEST_SEARCH_MAX_RESULTS_PER_TYPE` | 5 | 1–20 | 搜索每类上限 |
| `EZTEST_TODOS_MAX` | 50 | 1–200 | 待办返回上限 |
| `EZTEST_RATE_LIMIT_RPS` | 50 | 0–1000 | 每用户/IP 每秒令牌；0=关闭 |
| `EZTEST_RATE_LIMIT_BURST` | 100 | 1–5000 | 突发桶 |
| `EZTEST_RATE_LIMIT_SSE_MAX` | 10 | 1–100 | 每用户 SSE 并发 |
| `EZTEST_GC_ENABLED` | false | bool | 是否允许非 dry-run |
| `EZTEST_GC_RETENTION_DAYS` | 30 | 1–3650 | 保留天数 |
| `EZTEST_GC_BATCH_KEEP` | 3 | 1–50 | 最近批保护 |
| `EZTEST_INSTANCE_ID` | hostname+pid | ≤64 字符 | 实例标识 |
| `EZTEST_LEADER_LOCK_KEY` | 1163411512 | int64 | advisory lock key |
| `EZTEST_VISUAL_DIFF_THRESHOLD` | 0.005 | 0–0.1 | 默认差异阈值 |
| `EZTEST_VISUAL_BASELINE_MAX_BYTES` | 5242880 | 65536–20971520 | 基线图片上限 |
| `EZTEST_KNOWLEDGE_MAX_INJECT` | 5 | 0–20 | 注入条数 |
| `EZTEST_KNOWLEDGE_MAX_BYTES` | 4096 | 0–16384 | 注入字节上限 |
| `EZTEST_ANALYSIS_MAX_CASES` | 20 | 1–50 | 接线 P7 目标变量 |
| `EZTEST_SUPPLEMENT_MAX_CASES` | 5 | 1–20 | 接线 P7 目标变量 |

### 11.5 权限矩阵（新增）

| 能力 | 普通用户 | 管理员 |
|---|---|---|
| 链路/搜索/待办（review） | 读 | 读 |
| 待办 takeover/gate | 不显示 | 读/操作 |
| 知识草稿 | 创建/编辑自己的 | 全部 |
| 知识审批 | 否 | 是 |
| visual 参数 | 版本可编辑用户 | 是 |
| visual 基线审批 | 否 | 是 |
| Agent 中心配置 | 否（只读目录/会话） | 是 |
| 评测/GC/审计 | 否 | 是 |
| 事实修正 | 是（审计） | 是 |

---

## 12. 前端交付清单与页面映射（§2.9 增量）

| # | 交付物 | 阶段 | 验收锚点 |
|---|---|---|---|
| 19 | 版本切换器、全局搜索、待办中心 | P8 | 顶栏可切版本并保留 Tab；搜索分组/深链/快捷键；待办计数与深链 |
| 20 | Agent 中心、设置完善、空/错/重连/可访问性 | P8 | 13 角色目录/只读；审计可筛选；375/768 可导航；axe 无 serious |
| 24 | 探索链路 v2（ARTEX 级视觉/画布交互） | P8 | 截图对照清单；MiniMap/图例/Sheet/脉冲/跟随/截断/重连 |
| 25 | 脑图 Tab 嵌入（保留全屏路由） | P8 | `?tab=mindmap` 与 `/mindmap?case=` 一致 |
| 26 | 代码与变更完整版（批次时间线/影响用例/事实修正） | P8 | 批次时间线、受影响用例、未映射、质量区块 |
| 27 | 报告 revision 选择器/D49/stale 联动 | P8 | 可切 revision；证据变化后 stale |
| 28 | 质量中心（评测看板） | P8 | 指标/阈值/基线可见（admin） |
| 29 | 知识沉淀（版本内） | P8 | 草稿→审核→注入可追溯 |
| 30 | 移动端主导航、证据前后对比 | P8 | 375/768 导航；截图对比 |
| 31 | 视觉断言编辑/基线审批/差异查看 | P8 | 基线审批后执行；差异证据 |

> P9 交付物不在本表登记；P9 启动时另立规格。

---

## 13. 实施波次、并发与验收门禁

### 13.1 波次总览

| 波次 | 主题 | 依赖 | 门禁 |
|---|---|---|---|
| W0 | 基线护栏 | 无 | 全量门禁 + 缺陷重建复验 |
| W1 | 探索链路 v2 | W0 | 链路 E2E/视觉/性能/a11y；真实 LLM 截图 |
| W2 | 全局壳与版本详情 | W0；壳接口冻结 | 各功能 E2E + 深链 + 截图 |
| W3 | 管理面与体验硬化 | W0–W2 页面成形 | 权限/axe/视口截图 |
| W4 | 质量平台 | W0–W2；评测 ground truth 先冻结 | 评测门禁/视觉断言/知识闭环 |
| W5 | 非功能与最终交付 | W0–W4 | SLO/GC/备份/安全/文档/最终真实验收 |

### 13.2 任务表（并发标记 + 写作用域 + 依赖）

> 约定：**并发=是** 的任务可与同波次其他"是"任务同时执行；写作用域重叠的任务必须串行或由同一 owner 协调。实现阶段严格按此表并发。

| ID | 任务 | 并发 | 写作用域（示例） | 依赖 | 门禁 |
|---|---|---|---|---|---|
| T0.1 | 当前 HEAD 全量门禁复跑与修复 | 否 | 无（只读）/按失败修复 | — | test/acceptance/e2e 全绿 |
| T0.2 | CI workflow | 是 | `.github/workflows/**` | T0.1 | workflow 语法 + 静态验收 |
| T0.3 | 工程清理（gitignore/P0 文案/过时文档） | 是 | `.gitignore`、`frontend/src/features/shell/**`、README/deploy | T0.1 | lint/typecheck/静态 |
| T0.4 | 重建前端产物 + 缺陷 #1 复验 | 是 | `.tmp/**`、`docs/acceptance/p8/wave0/**` | T0.1 | 真实浏览器无报错截图 |
| T1.0 | React Flow 依赖 spike | 否 | `frontend/package.json`、`pnpm-lock.yaml`、spike 记录 | W0 | 构建/SSR/vitest/axe/200 节点通过 |
| T1.1 | 链路读模型后端 | 是 | `backend/internal/chain/**`、各上下文 chain provider、`internal/server/**` 装配、OpenAPI | T0.1 | 契约/查询预算/集成 |
| T1.2 | 前端链路数据层与正确性 | 是 | `frontend/src/features/execution/chain/use-exploration-chain.ts`、`types.ts`、`ExplorationTab.tsx` | T1.1（契约冻结） | 数据单测/降级/截断 |
| T1.3 | React Flow 画布与视觉 | 是 | `chain/ChainCanvas.tsx`、`nodes/**`、`edges/**`、`chain-tokens.ts`、`chain-layout.ts` | T1.0/T1.2 | 200 节点/截图/性能 |
| T1.4 | Sheet/图例/筛选/跟随/ARIA | 是 | `chain/ChainSheet.tsx`、`ChainLegend.tsx`、`ChainToolbar.tsx`、`ChainStates.tsx`（与 T1.3 文件不重叠） | T1.2 | 键盘/a11y/焦点 |
| T1.5 | 链路 E2E/视觉/axe/性能 | 是 | `frontend/e2e/**`、`docs/acceptance/p8/wave1/**` | T1.1–T1.4 | 全链路通过 |
| T1.6 | 契约守卫/错误文案/边界测试 | 是 | `backend/internal/platform/**`、version/delivery 测试 | T0.1 | 100/101/200 边界 |
| T2.1 | 版本切换器 + 待办中心 | 是 | `shell/Topbar.tsx`、`shell/todos/**`、`backend/internal/workspace/**`、`version`/`execution` provider | W0；T2.1 冻结 Topbar 插槽 | E2E/深链/权限 |
| T2.2 | 全局搜索 | 是 | `backend/internal/search/**`、各上下文 search provider、`shell/search/**` | T2.1 插槽 | 单测/契约/E2E |
| T2.3 | 脑图 Tab | 是 | `versions/VersionTabs.tsx`、`VersionDetailPage.tsx`、`mindmap/MindmapPage.tsx`、route | T2.3 先冻结 Tab/深链接口 | E2E/深链 |
| T2.4 | 代码与变更完整版 | 是 | `codeintel/**`、`code-intel/**`、迁移 | 独立 | E2E/迁移/质量 |
| T2.5 | 报告优化 | 是 | `delivery/report/**`、`delivery/ReportsTab.tsx` | 与 T2.3 协调 VersionDetailPage | E2E/stale |
| T3.1 | Agent 中心 | 是 | `agent-center/**`、`agent` 只读 API、`frontend/features/agent-console/**` | W2 壳 | 权限/E2E |
| T3.2 | 设置 + 审计查看器 + 审计覆盖 | 是 | `settings/**`、`audit/**`、各上下文写路径审计 | W2 | 权限/导出/E2E |
| T3.3 | 移动导航 + 响应式 | 是 | `shell/Sidebar.tsx`、`Topbar.tsx`、`AppShell.tsx` | W2 | 375/768 截图 |
| T3.4 | a11y + 空错重连 + 证据对比 | 是 | `components/ui/**`、`EvidenceViewer`、各页面状态（对比入口由 T3.4 先落，T4.3 只扩展 visual 差异展示） | W2 | axe/键盘/截图 |
| T4.1 | 评测语料 + runner + 基线 + 门禁 | 否（先冻结语料） | `eval/**`、`scripts/eval.ps1`、`quality/**`、`acceptance.ps1` | W2 | 指标达标/回归 |
| T4.2 | 代码事实质量 + 修正 + 影响度量 | 是 | `codeintel/**`、`code-intel/**`、`eval/ground-truth/**` | T4.1 ground truth | 单测/评测 |
| T4.3 | 视觉断言 | 是 | `mindmap/**`、`execution/**`、`executor/**`、迁移、`EvidenceViewer` | T4.1（visual 场景） | 真实 executor/分类 |
| T4.4 | 知识沉淀 | 是 | `version/knowledge/**`、`knowledge UI`、prompt 注入 | 独立 | 审核/注入/追溯 |
| T5.1 | 限流 + SLO | 是 | `server/**`、`scripts/slo.ps1`、配置 | W4 | SLO 报告/429 |
| T5.2 | GC/保留 | 是 | `execution/evidence/**`、`storage/**`、admin API/UI | W4 | dry-run/引用保护 |
| T5.3 | 备份恢复 | 是 | `scripts/backup.ps1`、`restore.ps1`、docs | W4 | 演练记录 |
| T5.4 | 多实例 leader + 执行器硬化 + 配置 + 报告快照 | 是 | `platform/leader/**`、`execution/**`、`executor/**`、`config/**` | W4 | 双实例集成/执行器 |
| T5.5 | 安全审计 + 审计覆盖 | 是 | `scripts/security-audit.ps1`、`audit/**`、安全头 | W4 | 审计报告/权限矩阵 |
| T5.6 | 文档 + Compose + P9 阶段 | 是 | `docs/**`、`deploy/**`、README、`implementation-phases.md` | W4 | 静态/文档审阅 |
| T5.7 | 最终真实验收 | 否 | `docs/acceptance/p8/**`、`.tmp/**` | T5.1–T5.6 | 全量门禁 + 真实 LLM + 浏览器 |

### 13.3 门禁

- **每任务**：定向单测/集成通过；lint/typecheck；不破坏既有测试；变更说明含参考 ARTEX `file:line`（如适用）与刻意差异。
- **每波**：`scripts/test.ps1` + 相关 E2E + 真实浏览器截图 + 验收记录；W1 额外视觉清单；W4 额外评测门禁；W5 额外 SLO/备份/安全。
- **最终**：`scripts/acceptance.ps1` + `scripts/e2e.ps1` + `scripts/eval.ps1` + `scripts/slo.ps1` + 备份演练 + 真实 LLM P8 脚本 + 真实浏览器全流程；产出 `docs/acceptance/p8/acceptance.md`。
- **未通过不进入下一波**；可裁剪范围但不可跳门禁。

---

## 14. 测试策略

- **Go 单元**：新服务/查询/校验/限流/leader/GC/视觉断言分类；同包 `_test.go`。
- **Go 集成**：真实 PostgreSQL；链路查询预算（SQL 计数）、search/todos、knowledge、version_change、visual baseline、multi-instance lock、GC 引用保护、报告 stale。
- **前端单元（Vitest）**：chain tokens/layout/节点/筛选/Sheet/键盘；搜索/待办/版本切换/脑图 Tab/报告 revision；a11y（axe-core）；React Flow 测试环境 polyfill（`ResizeObserver`/`DOMMatrixReadOnly`）。
- **E2E（Playwright）**：链路真实加载/问题节点/证据/深链/筛选/折叠/follow/键盘/截断；全局壳；脑图 Tab；报告 revision；Agent 中心权限；移动视口；视觉断言（真实 executor）；知识审核。
- **executor（node --test）**：`visual.compare` 像素比对、阈值、区域、环境指纹、工具调用上限、WS 白名单、超时收尾。
- **fake LLM 契约**：搜索/代办无需 LLM；评测/视觉断言/知识注入新增 fake 模式；P7 既有契约保持。
- **评测**：`scripts/eval.ps1` 确定性门禁 + 真实 LLM 报告。
- **SLO/备份/安全**：脚本化，纳入最终验收。
- **回归**：所有 P0–P7 既有测试必须继续通过；P7 路径不回归。

---

## 15. 验收策略（代用户执行真实人工验收）

1. **每波浏览器验收**：Lead 启动 API/Web/executor/DB，真实 Chrome（Playwright channel chrome）逐步操作；截图存 `docs/acceptance/p8/waveN/`；记录步骤、结果、异常。
2. **缺陷 #1 专项**：重建产物 → 真实 LLM 版本打开链路 → 断言无 `exploration-error`/`chain-issues-degraded` → 截图；用 >100 条数据验证截断横幅。
3. **链路视觉专项**：按 §5.4 清单逐项截图核对（图标卡/边标签箭头/图例/MiniMap/Sheet/脉冲/跟随/截断/空错重连/键盘）。
4. **真实 LLM**：`EZTEST_RUN_REAL_LLM=1` 运行新增 `scripts/acceptance-p8-llm.ps1` 与既有 P7 脚本；记录 token/耗时/降级；真实数据在浏览器走查。
5. **评测**：确定性门禁 + 真实 LLM 指标报告；失败项必须解释。
6. **SLO**：脚本测量并写报告；超阈值必须修复或记录例外。
7. **备份**：真实备份→恢复→校验；记录 RPO/RTO。
8. **安全**：审计脚本输出；秘密扫描 0 泄漏；权限矩阵 403/200 符合。
9. **最终记录**：`docs/acceptance/p8/acceptance.md`（结论、证据索引、已知限制、判定）；诚实标注无法验证项（Docker 未实跑、CI 未托管运行等）。
10. **无假数据**：截图/指标来自真实 API/LLM/executor；mock 仅用于确定性门禁并明确标注。

---

## 16. 风险与已知边界

| 风险 | 影响 | 缓解 |
|---|---|---|
| React Flow 可访问性/测试环境 | ARIA 回归、单测不稳 | T1.0 spike 确认集成方式；禁用其键盘导航；自有 ARIA 包装；axe 门禁；polyfill；**若集成受阻则修复适配层，不回退纯 SVG** |
| 范围过大 | 交付延期/质量下降 | 6 波独立验收；可裁剪范围不跳门禁；每波真实验收 |
| 真实 LLM 抖动 | 评测/验收不稳定 | 确定性门禁 + 真实 LLM 报告双轨 |
| 视觉断言环境差异 | 假失败 | 环境指纹；基线人工审批；不一致判 `inconclusive(environment)` |
| GC 误删 | 数据丢失 | 默认 dry-run；引用保护；sha256；二次确认；只删未引用 |
| 知识注入影响设计质量 | 结果偏差 | 仅批准条目、条数/字节预算、运行记录引用、可关闭 |
| 多实例锁测试复杂 | 隐蔽并发缺陷 | advisory lock 集成测试；单实例默认行为不变 |
| 本机无 Docker | 部署验证不完整 | Compose 静态校验 + 文档标注；有环境时补跑 |
| 无托管 CI | 回归依赖本地 | 新增 workflow 作为交付物；本地 acceptance 为最终门禁 |
| 评测语料有限 | 指标代表性不足 | P8 固定语料 + 基线；P9 扩展 |
| 旧验收记录分叉 | 误判 | README/验收记录标注历史；当前 HEAD 重跑 |

**已知边界**：Docker/Compose 未实跑；CI 未在托管环境运行；视觉断言仅 Chromium PNG；SLO 为本地口径；跨版本知识/PDF/非 PG/问题合并/多用户协作/多实例运维归 P9 候选（均需先澄清）；外部工单/CI-IM/环境编排/多租户为未排期（仅字段/非目标）；token 仍在 sessionStorage；LLM base_url 信任管理员。

---

## 17. 文档同步清单

| 文档 | 变更 |
|---|---|
| `docs/design/implementation-phases.md` | 新增 P9 阶段（§1 表格/依赖图/§2 详情/§4 里程碑），更新 P8 退出状态与 §2.9 行 24–31 |
| `docs/design/frontend-design.md` | §4.3.3 探索链路目标态改为 React Flow；§4.4 页面；§8 组件；§9 交付清单；§7 状态/可访问性更新 |
| `docs/design/system-blueprint.md` | P8 决策 D53–D69；实现注记；P9 登记；错误/事件/API 表更新 |
| `docs/openapi/api.yaml` | 新端点/错误/事件/DTO |
| `README.md` | P8 章节、命令、环境变量、验收记录修正 |
| `deploy/README.md` + `docker-compose.yml` | 证据/报告/导出卷；代码仓只读挂载示例；Dockerfile 目录 |
| `docs/deployment.md`（新） | 部署、反代、TLS、安全头、备份前置 |
| `docs/operations.md`（新） | 运维、GC、备份/恢复、SLO、leader、故障处理 |
| `docs/user-guide.md`（新） | 版本/需求/脑图/执行/问题/报告/搜索/待办/知识 |
| `docs/development.md`（新） | 架构、测试、E2E、评测、视觉断言、依赖治理 |
| `docs/acceptance/p8/**`（新） | 每波截图 + 最终验收记录 + SLO/备份/安全证据 |

---

## 18. 假设与待确认

设计阶段已确认：全量 P8 分批交付、真实验收授权、ARTEX 源码为基准、P7 遗留选 A + 新增 P9、A+R 画布且不回退纯 A。以下为实现假设，若与实际不符需在计划评审时调整：

1. `@xyflow/react` 12 与 Next 16.4/React 19.3 兼容（ARTEX 同栈证据）；T1.0 spike 用于确认 SSR/vitest/a11y/性能集成细节，**不通过则修适配层，不回退纯 SVG**。
2. 本机 Chrome/Edge 可用于真实浏览器验收；本机无 Docker。
3. `pixelmatch`/`pngjs`（MIT）可加入 executor；前端不新增运行时图库以外依赖。
4. PostgreSQL 17 支持 advisory lock 与现有迁移机制；无需 `pg_trgm`（搜索用 ILIKE）。
5. 真实 LLM 费用可控（DeepSeek flash）；真实 LLM 验收按波次限额执行。
6. SLO 阈值为本地口径（非生产 SLA），最终报告标注测量环境。

---

*文档版本：v1.0（2026-10-29）；与 P7 规格、`implementation-phases.md`、`frontend-design.md` 配套。*
