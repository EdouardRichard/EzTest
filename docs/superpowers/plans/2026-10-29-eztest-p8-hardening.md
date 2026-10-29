# EzTest P8 体验完善与硬化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 P8 设计规格交付“体验完善与硬化”：探索链路 v2（React Flow + 服务端读模型）、全局壳、版本详情增强、管理面、质量平台（评测/视觉断言/知识）、非功能硬化与文档，并通过真实人工验收。

**Architecture:** 后端新增 `chain`/`search`/`workspace`/`quality` 只读/质量服务与各上下文 Provider 适配器，新增迁移与事件；前端链路改为 React Flow 画布（保留 `chain-layout`/ARIA/数据层）；评测、SLO、备份、安全由脚本驱动；每波独立验收。

**Tech Stack:** Go 1.24 + PostgreSQL 17；Next.js 16.4 + React 19.3 + Tailwind 4 + `@xyflow/react` 12；Node 20+ / Playwright 1.64 executor；Vitest/Playwright/Biome；PowerShell 验收脚本。

**Spec:** `docs/superpowers/specs/2026-10-29-eztest-p8-hardening-design.md`

## Global Constraints

- 分支 `dev`；每任务一个 conventional commit；**不执行 `git push`**。
- 后端 Go 1.24，命令统一用便携工具链：`cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/...`。
- 前端 Next 16.4.3 / React 19.3.1 / Tailwind 4.3.3；`pnpm vitest run`、`pnpm lint`、`pnpm typecheck`。
- `@xyflow/react` 必须锁定精确 12.x 版本（T1.0 记录）；**不允许回退纯手写 SVG 方案**。
- 所有 P3+ 实体 ID 在 JSON/URL/事件中为十进制字符串；前端禁止 `Number()`。
- API 成功信封 `{"data": ...}`、统一错误信封、`X-Request-ID`；写接口带 `Idempotency-Key`。
- 迁移只追加：新文件按 `<context>_NNNN_<name>.sql` 命名并追加到 `backend/internal/db/migrations/order.txt`；禁止修改历史迁移。
- 凭据/Session/LLM Key/证据正文/知识正文不得进入日志、事件、审计、prompt、测试快照或前端页面；测试使用 fake LLM。
- P8 不新增业务能力；外部工单/CI-IM/环境编排/多租户/PDF/非 PG 目标库等未排期项**不得实现**。
- UI 文案中文；新增元素带稳定 `data-testid`；颜色/状态必须同时有图标或文本编码。
- TDD：先写失败测试并运行确认失败，再实现，再跑通过，再提交。
- 并发规则：任务标注 `并发=是` 时，只有写作用域完全不重叠才可并行；`并发=否` 必须串行。禁止两个 agent 同时改同一文件。
- 真实 LLM/真实 DB 只能由 gated 脚本运行；普通测试不得产生外部费用。
- 任何“完成”声明必须附命令输出或截图证据；最终验收记录写 `docs/acceptance/p8/acceptance.md`。

## Review Focus

1. **链路大数据量/截断**：>100 需求、>20 结果、>200 节点时不得静默丢失；`counts` 与 `truncated` 必须真实，latest attempt 正确。测试放在 Task 6/7/13。
2. **SSE 断线重连**：执行中刷新/断线后链路不得显示空态或重复请求风暴；重连后旧数据保留并补刷新。测试放在 Task 8/10/13。
3. **React Flow SSR/测试环境**：`next build`、SSR 首屏、jsdom 渲染不得因 `window`/`ResizeObserver` 崩溃；必须懒加载。测试放在 Task 5/9/10。
4. **权限边界**：普通用户对 Agent 中心配置、知识审批、评测、GC、审计、视觉基线审批必须只读或 403；admin 才可写。测试放在 Task 19/20/25/26/28/32。
5. **降级/失败安全**：无基线视觉断言、评测 fake LLM、搜索/待办后端失败、执行器不可用时，页面与接口不得假通过、不得崩。测试放在 Task 8/10/23/25。

---

## File Structure（新建/主要修改）

```text
backend/internal/
├─ chain/
│  ├─ application/{ports.go,service.go,service_test.go}
│  └─ interfaces/httpapi/{handlers.go,routes.go,handlers_test.go}
├─ search/
│  ├─ application/{ports.go,service.go,service_test.go}
│  └─ interfaces/httpapi/{handlers.go,routes.go,handlers_test.go}
├─ workspace/
│  ├─ application/{ports.go,service.go,service_test.go}
│  └─ interfaces/httpapi/{handlers.go,routes.go,handlers_test.go}
├─ quality/
│  ├─ domain/eval.go
│  ├─ application/{eval_service.go,eval_service_test.go,ports.go}
│  ├─ infrastructure/postgres/eval_repository.go
│  └─ interfaces/httpapi/{handlers.go,routes.go,handlers_test.go}
├─ version/
│  ├─ domain/knowledge.go
│  ├─ application/{knowledge_service.go,knowledge_service_test.go,ports.go}
│  ├─ infrastructure/postgres/knowledge_repository.go
│  └─ interfaces/httpapi/handlers_knowledge.go
├─ codeintel/
│  ├─ application/{version_change_service.go,fact_review_service.go}
│  ├─ infrastructure/postgres/{version_change_repository.go,fact_review_repository.go}
│  └─ interfaces/httpapi/handlers_change_quality.go
├─ execution/（视觉断言分类/证据 kind/GC/leader 接线）
├─ mindmap/（visual 断言/知识注入）
├─ platform/leader/leader.go
├─ server/（新路由/限流/安全头）
└─ db/migrations/（P8 迁移 7 个）

frontend/src/features/
├─ execution/chain/{chain-tokens.ts,chain-types.ts,use-exploration-chain.ts,chain-layout.ts,
│   ChainCanvas.tsx,ChainLegend.tsx,ChainToolbar.tsx,ChainSheet.tsx,ChainStates.tsx,
│   nodes/*.tsx,edges/ChainEdge.tsx,*.test.tsx}
├─ execution/components/ExplorationTab.tsx（组合层）
├─ shell/{VersionSwitcher.tsx,search/*,todos/*,Sidebar.tsx,Topbar.tsx,AppShell.tsx}
├─ versions/{VersionTabs.tsx,VersionDetailPage.tsx,VersionHeader.tsx}
├─ mindmap/{MindmapPage.tsx,VisualAssertionEditor.tsx}
├─ delivery/{ReportsTab.tsx,IssueDetail.tsx}
├─ agent-console/{AgentCenterPage.tsx,ReadonlyBanner.tsx}
├─ settings/{BasicConfigPanel.tsx,AuditLogPage.tsx}
├─ knowledge/{KnowledgePage.tsx,KnowledgePanel.tsx,ReviewDrawer.tsx}
├─ quality/{QualityPage.tsx,QualitySummary.tsx}
└─ components/ui/{Skeleton.tsx,ErrorState.tsx}

executor/src/{visual.mjs,server.mjs,limits.mjs}
eval/{README.md,corpus/*,ground-truth/*,baseline.json}
scripts/{eval.ps1,slo.ps1,backup.ps1,restore.ps1,security-audit.ps1,acceptance-p8-llm.ps1}
docs/{deployment.md,operations.md,user-guide.md,development.md}
docs/acceptance/p8/**
```

---

## Wave 0：基线护栏

### Task 1: 当前 HEAD 全量门禁复跑（并发=否）

**Files:**
- Read: `scripts/test.ps1`、`scripts/acceptance.ps1`、`scripts/e2e.ps1`
- Modify: 仅按失败项定向修复（记录在 `.tmp/p8-w0-gate.log`）

**Interfaces:**
- Consumes: 当前 HEAD `b86b057` + 未跟踪文件
- Produces: 门禁基线日志；后续所有任务以“全绿”为前提

- [ ] **Step 1: 记录工作区与分支**

Run: `git status --short; git branch --show-current`
Expected: 分支 `dev`；仅 `backend/.data/`、`docs/superpowers/specs/Untitled-1.md` 未跟踪（或已清理）。

- [ ] **Step 2: 运行快速门禁**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`
Expected: EXIT=0；若失败，记录首个失败包/测试名。

- [ ] **Step 3: 运行静态验收**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly`
Expected: `ACCEPTANCE PASSED`、EXIT=0。

- [ ] **Step 4: 运行 E2E**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`
Expected: 全部 passed（含 P7 5/5、execution 套件）；EXIT=0。

- [ ] **Step 5: 修复失败项（仅限回归/环境问题）**

对每个失败：先写能复现的最小测试或记录命令输出；修复后用同一命令复跑通过；不得顺手改业务行为。将日志写入 `.tmp/p8-w0-gate.log`。

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore(p8): establish baseline gates"
```

### Task 2: CI workflow（并发=是，写作用域 `.github/**`）

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: Task 1 的命令
- Produces: CI 配置（Go unit/integration、frontend lint/typecheck/test、executor test、静态验收）

- [ ] **Step 1: 写一个可本地校验的 workflow 结构测试（静态）**

Create `scripts/check-ci.ps1`：读取 `.github/workflows/ci.yml`，断言包含 `scripts/test.ps1`、`scripts/executor test`、`frontend` lint/typecheck/test 与 `acceptance.ps1 -StaticOnly` 关键词；不存在则 `exit 1`。

Run: `powershell.exe -File scripts/check-ci.ps1`
Expected: FAIL（文件不存在）。

- [ ] **Step 2: 创建 workflow**

内容要点：`on: [push, pull_request]`；Ubuntu；PostgreSQL 17 service（5433）；Node 22 + pnpm；Go 1.24；步骤依次 `go test ./...`、集成测试（设置 `EZTEST_TEST_DATABASE_URL`）、`pnpm install --frozen-lockfile`、`pnpm lint`、`pnpm typecheck`、`pnpm test`、`cd executor && pnpm test`、`scripts/acceptance.ps1 -StaticOnly`。不跑真实 LLM/E2E。

- [ ] **Step 3: 运行结构检查**

Run: `powershell.exe -File scripts/check-ci.ps1`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml scripts/check-ci.ps1
git commit -m "ci(p8): add baseline workflow"
```

### Task 3: 工程清理与 P0 文案（并发=是，写作用域 `.gitignore`、shell 文案、README/deploy）

**Files:**
- Modify: `.gitignore`、`frontend/src/features/shell/Topbar.tsx`、`Sidebar.tsx`、`AppShell.tsx`、`README.md`、`deploy/README.md`、`scripts/acceptance.ps1`（头部注释/检查项）
- Delete: `docs/superpowers/specs/Untitled-1.md`

**Interfaces:**
- Consumes: 无
- Produces: 干净工作区；P8 文案；不误导的验收脚本说明

- [ ] **Step 1: 写失败检查**

Create `scripts/check-clean.ps1`：断言 `.gitignore` 含 `.data/`、`backend/.data/`；grep 源码无 `P0 · 地基版本`/`P0 地基运行中`/`P0 平台地基`；断言 `Untitled-1.md` 不存在。Run → FAIL。

- [ ] **Step 2: 修改**

`.gitignore` 增加 `.data/` 与 `backend/.data/`；Topbar/Sidebar/AppShell 文案改为 `P8 · 体验硬化`（页脚 `EzTest · P8`）；README 的 P7“目标命令/不宣称通过”改为“已通过记录（见 acceptance.md）”；`acceptance.ps1` 头部 P0–P6 改 P0–P8；删除草稿文件。

- [ ] **Step 3: 运行检查 + lint/typecheck**

Run: `powershell.exe -File scripts/check-clean.ps1; cd frontend; pnpm lint; pnpm typecheck`
Expected: 全部 EXIT=0。

- [ ] **Step 4: Commit**

```bash
git add .gitignore frontend/src/features/shell README.md deploy/README.md scripts/acceptance.ps1 scripts/check-clean.ps1
git commit -m "chore(p8): clean workspace and P0 copy"
```

### Task 4: 重建产物 + 缺陷 #1 复验（并发=是，写作用域 `docs/acceptance/p8/wave0/**`、`.tmp/**`）

**Files:**
- Read: `frontend/src/features/execution/components/ExplorationTab.tsx`、`frontend/e2e/p7-issues-report.spec.ts`
- Create: `docs/acceptance/p8/wave0/defect-verification.md`、截图

**Interfaces:**
- Consumes: Task 1 服务可启动
- Produces: 缺陷 #1 在 HEAD 重建产物后不再复现的证据；旧产物复现根因记录

- [ ] **Step 1: 重建前端**

Run: `cd frontend; pnpm build`
Expected: `BUILD_ID` 时间晚于 `586e1d3` 提交时间。

- [ ] **Step 2: 启动依赖并跑链路 E2E**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-issues-report`
Expected: passed；日志无 `exploration-error`/`chain-issues-degraded`。

- [ ] **Step 3: 真实浏览器截图**

启动 API `:13333`、Web `:6868`（`scripts/dev-api.ps1`、`scripts/dev-web.ps1`），登录 admin，打开一个含数据的版本 → 探索链路；截图到 `docs/acceptance/p8/wave0/exploration-fixed.png`；确认无红条、问题节点存在。

- [ ] **Step 4: 写验收记录**

`defect-verification.md` 记录：旧 `.next` 构建时间、修复提交、HEAD 源码常量、E2E 结果、截图路径、结论。

- [ ] **Step 5: Commit**

```bash
git add docs/acceptance/p8/wave0
git commit -m "test(p8): verify exploration limit defect on rebuilt head"
```

---

## Wave 1：探索链路 v2

### Task 5: React Flow 依赖 spike（并发=否，W1 阻塞项）

**Files:**
- Modify: `frontend/package.json`、`pnpm-lock.yaml`
- Create: `docs/acceptance/p8/wave1/react-flow-spike.md`

**Interfaces:**
- Produces: 锁定的 `@xyflow/react` 版本与集成方式（懒加载、测试 polyfill、禁用内置键盘导航）

- [ ] **Step 1: 安装并锁定**

Run: `cd frontend; pnpm add @xyflow/react@12`
Expected: `package.json` 出现精确版本；`pnpm-lock.yaml` 更新。

- [ ] **Step 2: 写最小 SSR/构建测试**

Create `frontend/src/features/execution/chain/ChainCanvasSpike.test.tsx`：用 `dynamic(() => import("./ChainCanvasSpike"), { ssr:false })` 渲染一个 2 节点图；断言节点标题可见、无 console error。Vitest setup 增加 `ResizeObserver`/`DOMMatrixReadOnly` polyfill。

Run: `cd frontend; pnpm vitest run src/features/execution/chain/ChainCanvasSpike.test.tsx`
Expected: 初次 FAIL（组件不存在/缺 polyfill）。

- [ ] **Step 3: 实现最小画布**

Create `ChainCanvasSpike.tsx`：`"use client"`；`ReactFlow` + `Background variant="dots"` + `MiniMap` + `Controls`；自定义节点渲染标题；`disableKeyboardA11y`；容器固定 `width/height`。仅 spike 版，后续 Task 10 扩展为正式 `ChainCanvas.tsx`。

- [ ] **Step 4: 运行测试 + build + axe 冒烟**

Run: `cd frontend; pnpm vitest run src/features/execution/chain/ChainCanvasSpike.test.tsx; pnpm build`
Expected: PASS；build 成功。

- [ ] **Step 5: 记录 spike 结论**

`react-flow-spike.md`：版本、懒加载方式、polyfill、构建时间、200 节点渲染耗时；结论“采用/需调整”，**不写回退纯 SVG 选项**。

- [ ] **Step 6: Commit**

```bash
git add frontend/package.json frontend/pnpm-lock.yaml frontend/src/features/execution/chain docs/acceptance/p8/wave1/react-flow-spike.md
git commit -m "feat(p8): add react flow spike"
```

### Task 6: 链路读模型聚合服务（并发=是，后端；写作用域 `backend/internal/chain/**`）

**Files:**
- Create: `backend/internal/chain/application/ports.go`、`service.go`、`service_test.go`
- Create: `backend/internal/chain/interfaces/httpapi/handlers.go`、`routes.go`、`handlers_test.go`

**Interfaces:**
- Consumes: 无（Provider 由 Task 7 实现）
- Produces:
  - `type Query struct { VersionID, BatchID, RequirementID, ResultStatus, IssueStatus, IssueSeverity, Search string; AbnormalOnly, IncludeSupplement, RevalidationOnly bool; Limit, RequirementLimit int }`
  - `type Snapshot struct { VersionID string; Batch BatchDTO; Nodes []NodeDTO; Edges []EdgeDTO; Counts CountsDTO; Truncated TruncatedDTO; GeneratedAt time.Time }`
  - `type Service struct{ ... }; func NewService(p Providers) *Service; func (s *Service) Build(ctx context.Context, q Query) (Snapshot, error)`
  - HTTP: `GET /api/versions/{versionId}/exploration-chain`

- [ ] **Step 1: 写失败测试（服务层）**

`service_test.go` 用 fake providers 断言：
1. 同一 case 多条 result 只保留 attempt 最大者；
2. issue 有 `caseRunID` 命中最新结果 → `result_issue` 边；否则 `case_issue`；
3. `Limit=2` 时 `Truncated.Cases=true` 且 `Counts.Cases=5`（真实总数）；
4. 非法 `Limit=0` 返回 `ErrInvalidArgument{Field:"limit"}`；
5. 空版本返回 `ErrNotFound`。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/application`
Expected: FAIL（包/类型不存在）。

- [ ] **Step 2: 实现 ports/service**

按规格 §5.1/§5.2：
- Providers：`VersionProvider`（Batch/Requirements）、`MindmapProvider`（Cases/Coverage）、`ExecutionProvider`（LatestResults）、`DeliveryProvider`（Issues）、`CodeIntelProvider`（Impact）。
- 组装节点 ID：`requirement:<id>`/`case:<id>`/`result:<caseRunId>`/`issue:<id>`；边 ID 见规格。
- 过滤：`RequirementID`、`ResultStatus`、`AbnormalOnly`、`IssueStatus`、`IssueSeverity`、`Search`、`IncludeSupplement`、`RevalidationOnly`；全部在服务端应用。
- 截断：先按层收集，再按 `RequirementLimit`/`Limit` 截断并设置 `Truncated`；`Counts` 用未截断总数。
- 排序：需求 `SortOrder,ID`；用例 `SortOrder,ID`；结果 `Attempt desc,ID`；问题严重级 rank + 创建时间。
- 错误类型：`ErrInvalidArgument`（field/reason）、`ErrNotFound`。

- [ ] **Step 3: 运行服务测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/application`
Expected: PASS。

- [ ] **Step 4: 写 HTTP 失败测试**

`handlers_test.go`（httptest + fake Service）：200 信封含 `nodes/edges/counts/truncated`；`limit=0`→400 `invalid_argument` + `details.field=limit`；未知版本→404；非 GET→405。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/interfaces/httpapi`
Expected: FAIL。

- [ ] **Step 5: 实现 handler/routes + 注册**

`handlers.go` 解析查询参数（`strconv.Atoi`，范围 50–2000/1–500，默认 300/100）；调用 Service；错误映射。`routes.go` 暴露 `RegisterRoutes(mux, handler)`。在 `backend/internal/server/server.go` 的 `Dependencies` 增加 `Chain *chainhttp.Handlers`，`New` 中注册；`cmd/eztest/main.go` 接线（Task 7 完成后编译）。

- [ ] **Step 6: 运行 HTTP 测试 + vet**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/...; ..\.tools\go\bin\go.exe vet ./internal/chain/...`
Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add backend/internal/chain backend/internal/server/server.go
git commit -m "feat(p8): add exploration chain read model"
```

### Task 7: 各上下文 Chain Provider + 装配（并发=是，写作用域各上下文 `infrastructure/chain/**`、`cmd/eztest/main.go`）

**Files:**
- Create: `backend/internal/version/infrastructure/chain/reads.go`、测试
- Create: `backend/internal/mindmap/infrastructure/chain/reads.go`、测试
- Create: `backend/internal/execution/infrastructure/chain/reads.go`、测试
- Create: `backend/internal/delivery/infrastructure/chain/reads.go`、测试
- Create: `backend/internal/codeintel/infrastructure/chain/reads.go`、测试
- Modify: `backend/cmd/eztest/main.go`（构造 providers/service/handlers，加入 `server.Dependencies`）

**Interfaces:**
- Consumes: Task 6 的 `chain/application` 类型
- Produces: 各 Provider 实现；SQL 批量查询（每 Provider ≤2 条 SQL）；`server.Dependencies.Chain` 已接线

- [ ] **Step 1: 写查询预算测试（集成）**

`backend/internal/chain/integration/query_budget_test.go`：真实 PG，插入 100 需求/300 用例/300 结果/100 问题/2 影响；用 `pgx` query tracer 计数；调用 Service.Build；断言 SQL 总数 ≤10，且结果 latest attempt 正确。Run → FAIL（providers 未实现）。

- [ ] **Step 2: 实现 version provider**

`Reads`：Batch（1 SQL）、Requirements（1 SQL，含 open question count 聚合，不使用逐行 Count）。返回 `[]chainapp.RequirementRow`、真实 total。

- [ ] **Step 3: 实现 mindmap provider**

Cases（1 SQL：`case_node` + `case_source` 聚合 `requirementIDs` + steps/assertions 计数子查询）、Coverage（1 SQL）。只返回链路字段。

- [ ] **Step 4: 实现 execution provider**

LatestResults：`DISTINCT ON (case_node_id)` 按 attempt desc,id desc；1 SQL；`evidence_count` 用 `evidence` 聚合子查询；`batchId` 过滤当前批。

- [ ] **Step 5: 实现 delivery/codeintel provider**

Issues：1 SQL + `issue_ref` 聚合；返回 refs evidence 计数、caseRun/caseNode。Impact：1 SQL 读最近 analysis 候选（selected 或 high/medium）。

- [ ] **Step 6: 装配 + 运行集成**

在 `main.go` 构造 `chainapp.NewService(chainapp.Providers{...})`，`chainhttp.NewHandler`，加入 `server.Dependencies{Chain: ...}`；编译并跑：

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/chain/integration`
Expected: PASS（查询数 ≤10）。

- [ ] **Step 7: Commit**

```bash
git add backend/internal/*/infrastructure/chain backend/internal/chain/integration backend/cmd/eztest/main.go
git commit -m "feat(p8): wire chain providers"
```

### Task 8: 前端链路数据层与正确性修复（并发=是，写作用域 `execution/chain/use-exploration-chain.ts`、`chain-types.ts`、`ExplorationTab.tsx`）

**Files:**
- Create: `frontend/src/features/execution/chain/chain-types.ts`、`use-exploration-chain.ts`、`use-exploration-chain.test.tsx`
- Modify: `frontend/src/features/execution/components/ExplorationTab.tsx`（数据层替换；视觉层 Task 9/10）
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（深链参数透传）

**Interfaces:**
- Consumes: Task 6 API；`stream`（P3 SSE）
- Produces: `useExplorationChain(versionId, params, stream)` 返回 `{data, loading, error, streamStatus, refresh}`；URL 参数 `node/batch/req/status/abnormal/q`；证据按 `caseRunId` 懒加载

- [ ] **Step 1: 写失败测试**

`use-exploration-chain.test.tsx`：
1. 初始请求 URL 含 `limit=300`，不发送 >2000；
2. SSE `case.status_changed` 到达后 500ms 内只触发 1 次刷新（fake timers）；
3. 请求失败时保留旧 `data` 且 `error` 非空；
4. `truncated=true` 时返回标记；
5. 卸载后不 setState（abort）。

Run: `cd frontend; pnpm vitest run src/features/execution/chain/use-exploration-chain.test.tsx`
Expected: FAIL。

- [ ] **Step 2: 实现 hook/types**

`chain-types.ts` 按规格 §5.1 定义 DTO；`use-exploration-chain.ts`：`apiGet` + AbortController + 500ms debounce + stream 订阅（复用 `useVersionStream` 的 stream 对象）+ `refresh`；错误保留旧数据。

- [ ] **Step 3: 替换 ExplorationTab 数据源**

删除 requirements/mindmap/case-runs/issues 四路拼接；改用 hook；保留筛选 UI 但映射到 query；保留 `data-testid`；问题层失败显示“问题数据不可用”，截断显示横幅；Sheet 证据按钮按 `caseRunId` 取详情。

- [ ] **Step 4: 深链**

`VersionDetailPage` 读取 `node/req/status/abnormal/q` 并传给 Tab；`node` 打开对应 Sheet；`evidence` 参数打开 EvidenceViewer（已有）。

- [ ] **Step 5: 运行测试**

Run: `cd frontend; pnpm vitest run src/features/execution/chain src/features/execution/components/ExplorationTab.test.tsx`
Expected: PASS（旧 mock 测试需按新 hook 适配）。

- [ ] **Step 6: Commit**

```bash
git add frontend/src/features/execution frontend/src/features/versions/VersionDetailPage.tsx
git commit -m "feat(p8): load exploration chain from read model"
```

### Task 9: 链路 tokens/布局/节点（并发=是，写作用域 `chain/chain-tokens.ts`、`chain-layout.ts`、`nodes/**`）

**Files:**
- Create: `frontend/src/features/execution/chain/chain-tokens.ts`、`nodes/RequirementNode.tsx`、`CaseNode.tsx`、`ResultNode.tsx`、`IssueNode.tsx`
- Move/Modify: `frontend/src/features/execution/chain-layout.ts` → `chain/chain-layout.ts`；测试同步移动
- Create: 对应 `*.test.tsx`

**Interfaces:**
- Consumes: Task 8 `chain-types`
- Produces: `CHAIN_TOKENS`（节点/边/状态/严重级颜色与文案）；`layoutChain(nodes, opts)` 返回 `{nodes: PositionedNode[], edges: LaidEdge[], bounds}`；四个节点组件 props `{data, selected}`

- [ ] **Step 1: 写失败测试**

`chain-tokens.test.ts`：每条状态/严重级有 icon+label+color，颜色不重复且与 `StatusBadge` 语义一致；`RequirementNode.test.tsx` 断言标题/状态/未覆盖徽章；`CaseNode` 断言 AI 补充角标/重验琥珀环/影响标记；`ResultNode` 断言 attempt/耗时/证据数/失败分类；`IssueNode` 断言严重级/状态/证据数。

Run: `cd frontend; pnpm vitest run src/features/execution/chain`
Expected: FAIL。

- [ ] **Step 2: 实现 tokens + 节点**

用 lucide-react 图标；颜色读 CSS 变量/Tailwind token；`data-testid="chain-node-<kind>"`；`aria-label` 包含标题+状态；`memo`。

- [ ] **Step 3: 移动并适配布局**

`layoutChain` 输入改为 `chain-types` 节点；保持确定性四层、折叠、bounds；更新测试 import。

- [ ] **Step 4: 运行测试**

Run: `cd frontend; pnpm vitest run src/features/execution/chain`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/execution/chain
git commit -m "feat(p8): add chain tokens and node cards"
```

### Task 10: React Flow 画布/边/MiniMap/跟随（并发=是，写作用域 `chain/ChainCanvas.tsx`、`edges/**`）

**Files:**
- Create: `frontend/src/features/execution/chain/ChainCanvas.tsx`、`edges/ChainEdge.tsx`、`ChainCanvas.test.tsx`
- Create: `frontend/src/features/execution/chain/use-chain-viewport.ts`

**Interfaces:**
- Consumes: Task 9 节点/布局/tokens
- Produces: `<ChainCanvas snapshot={Snapshot} selectedId onSelect collapsed onToggleCollapse follow />`；`data-testid="chain-canvas"/"chain-minimap"/"chain-edge-<kind>"`

- [ ] **Step 1: 写失败测试**

1. 渲染 200 节点不抛错；`onlyRenderVisibleElements` 生效（可见节点 ≤ 总数）；
2. 边有 `data-testid`、label 与 marker；
3. 点击节点触发 `onSelect`；
4. running 节点有脉冲类；`prefers-reduced-motion` 下无动画类；
5. `next/dynamic ssr:false` 导入不触发 SSR。

Run: `cd frontend; pnpm vitest run src/features/execution/chain/ChainCanvas.test.tsx`
Expected: FAIL。

- [ ] **Step 2: 实现画布**

`ReactFlow` + `Background dots` + `MiniMap` + `Controls` + 自定义 `nodeTypes`/`edgeTypes`；`disableKeyboardA11y`；`onlyRenderVisibleElements`；位置来自 `layoutChain`；`fitView` 仅初始化；`follow` 用 `useReactFlow().setCenter`，用户手动操作后暂停。

- [ ] **Step 3: 实现边**

语义色 + label + markerEnd + 目标 running 时 `animated`；`impact_case` 虚线琥珀；reduced-motion 关闭动画。

- [ ] **Step 4: 运行测试 + build**

Run: `cd frontend; pnpm vitest run src/features/execution/chain/ChainCanvas.test.tsx; pnpm build`
Expected: PASS；build 成功。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/execution/chain
git commit -m "feat(p8): render chain with react flow"
```

### Task 11: 图例/工具栏/Sheet/状态/键盘（并发=是，写作用域 `chain/ChainLegend.tsx`、`ChainToolbar.tsx`、`ChainSheet.tsx`、`ChainStates.tsx`）

**Files:**
- Create: `frontend/src/features/execution/chain/ChainLegend.tsx`、`ChainToolbar.tsx`、`ChainSheet.tsx`、`ChainStates.tsx` 及测试
- Modify: `frontend/src/features/execution/components/ExplorationTab.tsx`（组合层）

**Interfaces:**
- Consumes: Task 10 画布
- Produces: 图例/筛选/搜索/截断/流状态/Sheet；`role=tree` 键盘导航；`data-testid` 见规格

- [ ] **Step 1: 写失败测试**

1. 图例含 4 类节点 + 5 类边 + 空态文案；
2. 工具栏筛选变化触发 `onChange`（需求/状态/仅异常/严重级/AI 补充/重验/搜索）；
3. 截断横幅 `chain-truncated` 文案含真实总数；
4. 流状态 `chain-stream-status` 在断开时显示“重连中”；
5. Sheet：打开聚焦、Esc 关闭、焦点归还、证据/深链按钮；
6. 键盘 ↑↓/←/→/Home/End/Enter/Esc 行为。

Run: `cd frontend; pnpm vitest run src/features/execution/chain`
Expected: FAIL。

- [ ] **Step 2: 实现组件**

Sheet 用 `role="dialog" aria-modal` + 焦点陷阱；图例 overlay；工具栏按钮 ≥44px；状态组件含骨架/空/错/降级；键盘导航基于 `visibleIds` 树序；提供“列表视图”降级（类型/标题/状态/关联/证据，键盘可达）。

- [ ] **Step 3: 组合进 ExplorationTab**

布局改为全宽画布 + overlay；删除右侧 320px 固定列；筛选条保留；问题未关联列表放 Sheet/底部。

- [ ] **Step 4: 运行测试**

Run: `cd frontend; pnpm vitest run src/features/execution/chain src/features/execution/components/ExplorationTab.test.tsx`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/execution
git commit -m "feat(p8): add chain legend sheet and states"
```

### Task 12: limit 契约守卫/错误文案/边界测试（并发=是，写作用域 `backend/internal/platform/**`、version/delivery 测试）

**Files:**
- Modify: `backend/internal/platform/pagination.go`、`errors_test.go`
- Modify: `backend/internal/version/interfaces/httpapi/helpers.go`、`handlers_requirements_test.go`
- Modify: `backend/internal/delivery/interfaces/httpapi/routes.go`、`handlers_issue_test.go`

**Interfaces:**
- Consumes: 既有 `ParseLimit`
- Produces: 非整数/超范围分开文案 + `details{field,min,max}`；既有状态码不变

- [ ] **Step 1: 写失败测试**

`TestParseLimit` 增加 `"200"` 非法；新增 `TestLimitErrorMessages`：非整数 → "must be an integer"，101/200 → "must be between 1 and 100" + details。requirements `?limit=100`→200、`?limit=200`→400 `invalid_limit`；issues `?limit=200`→422。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/platform ./internal/version/interfaces/httpapi ./internal/delivery/interfaces/httpapi`
Expected: FAIL（文案/details 未实现）。

- [ ] **Step 2: 实现**

`ParseLimit` 返回带 `min/max` 的错误；HTTP 层 `details` 写入错误信封（沿用现有结构）。不改变 400/422 映射。

- [ ] **Step 3: 运行测试**

Run: 同上
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/platform backend/internal/version/interfaces/httpapi backend/internal/delivery/interfaces/httpapi
git commit -m "fix(p8): clarify limit errors and pin bounds"
```

### Task 13: 链路 E2E/视觉/axe/性能（并发=是，写作用域 `frontend/e2e/**`、`docs/acceptance/p8/wave1/**`）

**Files:**
- Create: `frontend/e2e/p8-exploration-chain.spec.ts`
- Create: `frontend/src/features/execution/chain/chain-a11y.test.tsx`
- Create: `scripts/slo-chain.ps1`（后续 Task 27 复用）
- Modify: `scripts/e2e.ps1`（加入 p8 spec）

**Interfaces:**
- Consumes: Task 6–11
- Produces: 真实链路 E2E 与截图；axe 报告；200 节点性能数据

- [ ] **Step 1: 写 E2E 失败用例**

断言：打开 `?tab=exploration`；无 `exploration-error`；需求/用例/结果/问题节点存在；点击问题节点 Sheet 显示证据/深链；筛选“仅异常”只留异常；折叠/展开计数；截断 fixture（>100 需求）显示 `chain-truncated`；断线模拟显示“重连中”。

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p8-exploration-chain`
Expected: FAIL（spec 未注册/元素缺失）。

- [ ] **Step 2: 实现 fixture/断言并注册**

在 e2e 中创建/复用版本数据；截图存 `docs/acceptance/p8/wave1/`；`expectNoConsoleErrors`、`expectNoSecretInPage` 沿用。

- [ ] **Step 3: axe + 性能**

`chain-a11y.test.tsx`：axe 无 serious/critical；200 节点渲染 + 键盘全流程。`slo-chain.ps1` 测 200 节点 P95。

- [ ] **Step 4: 运行并保存证据**

Run: e2e + vitest + slo 脚本
Expected: PASS；截图与 metrics.json 写入验收目录。

- [ ] **Step 5: Commit**

```bash
git add frontend/e2e/p8-exploration-chain.spec.ts frontend/src/features/execution/chain/chain-a11y.test.tsx scripts/e2e.ps1 scripts/slo-chain.ps1 docs/acceptance/p8/wave1
git commit -m "test(p8): add exploration chain e2e and a11y"
```

## Wave 2：全局壳与版本详情

### Task 14: 版本切换器 + 待办中心（并发=是；后端 `workspace/**`、前端 `shell/{VersionSwitcher,todos}/**`）

**Files:**
- Create: `backend/internal/workspace/application/ports.go`、`service.go`、`service_test.go`
- Create: `backend/internal/workspace/interfaces/httpapi/handlers.go`、`routes.go`、`handlers_test.go`
- Create: `backend/internal/version/infrastructure/workspace/reads.go`、`backend/internal/execution/infrastructure/workspace/reads.go`
- Create: `frontend/src/features/shell/VersionSwitcher.tsx`、`frontend/src/features/shell/todos/TodoButton.tsx`、`TodoPanel.tsx`、`useTodos.ts` 及测试
- Modify: `frontend/src/features/shell/Topbar.tsx`、`backend/internal/server/server.go`、`backend/cmd/eztest/main.go`

**Interfaces:**
- Consumes: `GET /api/versions`（既有）；`version.status=reviewing`；`case_run.pending_takeover`
- Produces: `GET /api/todos` → `{items:[{kind,title,versionId,versionName,severity,createdAt,url}],counts}`；`<VersionSwitcher />`、`<TodoButton />`

- [ ] **Step 1: 写失败测试（后端）**

`service_test.go`：review provider 返回 2 条；takeover provider 返回 1 条；普通用户结果不含 takeover；`limit` 默认 50。`handlers_test.go`：GET 200 信封；未登录 401；普通用户 counts.takeover=0（或分组不出现）。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/workspace/...`
Expected: FAIL。

- [ ] **Step 2: 实现 workspace + providers + 装配**

Service 聚合去重（同版本 review 只一条）；Provider 用 1–2 条 SQL；HTTP handler 注入 `isAdmin`；server/main 接线。

- [ ] **Step 3: 写失败测试（前端）**

`VersionSwitcher.test.tsx`：最近版本列表、搜索过滤、选择后 `router.push` 保留 `tab`、空态。`TodoPanel.test.tsx`：分组/计数/深链/加载/错误/空态；普通用户无 takeover 分组。

Run: `cd frontend; pnpm vitest run src/features/shell`
Expected: FAIL。

- [ ] **Step 4: 实现前端**

Topbar 增加 `VersionSwitcher` 与 `TodoButton`（铃铛 + 计数）；`useTodos` 30s 轮询 + 路由变化刷新；localStorage `eztest.recentVersions`（最多 8）；错误不阻塞壳。

- [ ] **Step 5: 运行测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/workspace/...; cd ..\frontend; pnpm vitest run src/features/shell`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add backend/internal/workspace backend/internal/*/infrastructure/workspace backend/internal/server/server.go backend/cmd/eztest/main.go frontend/src/features/shell
git commit -m "feat(p8): add version switcher and todo centre"
```

### Task 15: 全局搜索（并发=是；后端 `search/**`、前端 `shell/search/**`）

**Files:**
- Create: `backend/internal/search/application/ports.go`、`service.go`、`service_test.go`
- Create: `backend/internal/search/interfaces/httpapi/handlers.go`、`routes.go`、`handlers_test.go`
- Create: `backend/internal/version/infrastructure/search/reads.go`
- Create: `backend/internal/mindmap/infrastructure/search/reads.go`
- Create: `backend/internal/delivery/infrastructure/search/reads.go`
- Create: `frontend/src/features/shell/search/SearchCommand.tsx`、`useGlobalSearch.ts`、测试
- Modify: `frontend/src/features/shell/Topbar.tsx`（搜索插槽，Task 14 已预留）

**Interfaces:**
- Consumes: `Get versions/requirements/cases/issues`
- Produces: `GET /api/search?q=&types=&limit=` → `{groups:[{type,items:[{id,title,subtitle,versionId,versionName,status,url}]}],total,truncated}`；`<SearchCommand />`（Cmd/Ctrl+K）

- [ ] **Step 1: 写失败测试（后端）**

`service_test.go`：q 空 → `ErrInvalidArgument{field:"q"}`；types 过滤只返回指定组；每类最多 limit；Provider 错误时返回部分结果 + `truncated=true`（或聚合错误？选择：单 Provider 失败记日志并继续，响应 `degraded:true`）。`handlers_test.go`：200/400/401。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/search/...`
Expected: FAIL。

- [ ] **Step 2: 实现**

Provider 各 1 条 ILIKE SQL（title/name，`limit`，按 updated_at desc）；URL 深链：版本 `/versions/{id}?tab=overview`、需求 `?tab=requirements&requirement=`、用例 `?tab=mindmap&case=`、问题 `?tab=issues&issue=`；不返回敏感字段。Service 并行调用、单类失败降级。

- [ ] **Step 3: 写失败测试（前端）**

`SearchCommand.test.tsx`：输入 300ms 防抖；分组渲染；键盘 ↑↓/Enter/Esc；空结果/错误；最近搜索 5 条；`Cmd+K` 打开。

Run: `cd frontend; pnpm vitest run src/features/shell/search`
Expected: FAIL。

- [ ] **Step 4: 实现前端**

`SearchCommand` 用现有 Radix primitives；请求取消；深链 `router.push`；Topbar 搜索框点击/快捷键打开。

- [ ] **Step 5: 运行测试 + lint**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/search/...; cd ..\frontend; pnpm vitest run src/features/shell/search; pnpm typecheck`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add backend/internal/search backend/internal/*/infrastructure/search backend/internal/server/server.go backend/cmd/eztest/main.go frontend/src/features/shell
git commit -m "feat(p8): add global search"
```

### Task 16: 脑图回版本详情 Tab（并发=是；写作用域 `versions/{VersionTabs,VersionDetailPage}.tsx`、`mindmap/MindmapPage.tsx`）

**Files:**
- Modify: `frontend/src/features/versions/VersionTabs.tsx`、`VersionDetailPage.tsx`、`frontend/src/app/(app)/versions/[id]/mindmap/page.tsx`
- Modify: `frontend/src/features/mindmap/MindmapPage.tsx`（`embedded`、`initialCaseId`）
- Create: `frontend/src/features/mindmap/MindmapPage.embedded.test.tsx`

**Interfaces:**
- Consumes: 既有 `MindmapPage`
- Produces: Tab 顺序 10 项；`?tab=mindmap&case=`；全屏路由读取 `?case=`

- [ ] **Step 1: 写失败测试**

断言：Tab 列表含“用例脑图”且顺序在“需求与澄清”之后；`?tab=mindmap` 渲染嵌入版（无全屏返回按钮）；`?case=<nodeId>` 选中节点；非法 case 显示提示不崩；全屏路由 `?case=` 生效。

Run: `cd frontend; pnpm vitest run src/features/mindmap src/features/versions`
Expected: FAIL。

- [ ] **Step 2: 实现**

`MindmapPage` 增加 props `{embedded?:boolean, initialCaseId?:string}`；嵌入时隐藏页头、高度 100%；Tab 注册；深链读写由 `VersionDetailPage` 统一。

- [ ] **Step 3: 运行测试 + build**

Run: `cd frontend; pnpm vitest run src/features/mindmap src/features/versions; pnpm build`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add frontend/src/features/mindmap frontend/src/features/versions frontend/src/app/\(app\)/versions
git commit -m "feat(p8): embed mindmap in version tabs"
```

### Task 17: `version_change` 与代码与变更完整版（并发=是；写作用域 `codeintel/**`、`code-intel/CodeChangesTab.tsx`）

**Files:**
- Create: `backend/internal/db/migrations/codeintel_0006_version_change.sql`
- Create: `backend/internal/codeintel/application/version_change_service.go`、测试
- Create: `backend/internal/codeintel/infrastructure/postgres/version_change_repository.go`、测试
- Modify: `backend/internal/codeintel/interfaces/httpapi/{handlers,routes}.go`（`GET /api/versions/{id}/changes` 返回快照/时间线）
- Modify: `frontend/src/features/code-intel/CodeChangesTab.tsx` 及测试

**Interfaces:**
- Consumes: 批次 `base_commit/head_commit/base_fallback_reason`；P4 change set
- Produces: `version_change` 表；`code.changed` 事件；`GET /changes` 兼容旧响应 + 新增 `batchTimeline`

- [ ] **Step 1: 写失败测试（后端）**

迁移测试：表存在、唯一键；service 测试：建批生成快照、重复 upsert 幂等、无代码仓返回空；`handlers_test`：`changes` 含 `batchTimeline` 且旧字段保留。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/codeintel/...`
Expected: FAIL。

- [ ] **Step 2: 实现迁移/服务/路由**

按规格 §4.2/§7.2；`items` 只存摘要；`history_rewritten` 来自批次；发布 `code.changed`（detail 不含代码正文）；测试断言事件 detail 只有 batch/commit/计数。

- [ ] **Step 3: 写失败测试（前端）**

`CodeChangesTab.test.tsx`：批次时间线、文件/实体、受影响用例、未映射、空态“本批无代码变更”、错误重试。

Run: `cd frontend; pnpm vitest run src/features/code-intel`
Expected: FAIL。

- [ ] **Step 4: 实现前端 + 运行**

Run: `cd frontend; pnpm vitest run src/features/code-intel`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/db/migrations backend/internal/codeintel frontend/src/features/code-intel
git commit -m "feat(p8): add version change snapshots"
```

### Task 18: 报告 revision 选择器/D49/stale 联动（并发=是；写作用域 `delivery/report*`、`delivery/ReportsTab.tsx`）

**Files:**
- Modify: `backend/internal/delivery/application/report_service.go`、`report_service_test.go`
- Modify: `backend/internal/delivery/domain/report.go`（如需指纹字段）
- Modify: `frontend/src/features/delivery/components/ReportsTab.tsx` 及测试

**Interfaces:**
- Consumes: 既有 `report` revision/指纹
- Produces: `content.batch.baseFallbackReason/historyRewritten`；revision 列表 UI；证据计数纳入 fingerprint

- [ ] **Step 1: 写失败测试**

1. 生成报告后 `content.batch.historyRewritten` 与批次一致；
2. 新增证据（计数变化）→ `GET report` 返回 stale=true 且只发一次 `report.stale`；
3. 旧 revision 不变；重新生成 revision+1。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
Expected: FAIL。

- [ ] **Step 2: 实现指纹/快照**

`input_fingerprint` 纳入每用例 evidence 计数/失败分类；读时比较；事件 detail 加 `reason`；D49 字段写入快照。

- [ ] **Step 3: 前端 revision 选择器**

`ReportsTab` 增加 revision 下拉（`?report=` 同步）、stale 横幅、D49 字段展示；测试。

- [ ] **Step 4: 运行测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...; cd ..\frontend; pnpm vitest run src/features/delivery`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/delivery frontend/src/features/delivery
git commit -m "feat(p8): add report revision selector and stale linkage"
```

---

## Wave 3：管理面与体验硬化

### Task 19: Agent 中心（并发=是；写作用域 `agent` 只读 API、`agent-console/**`）

**Files:**
- Create: `backend/internal/agent/interfaces/httpapi/handlers_center.go`、`routes_center.go`、测试
- Modify: `backend/internal/agent/application/...`（只读目录/统计服务）
- Create: `frontend/src/features/agent-console/AgentCenterPage.tsx`、`ReadonlyBanner.tsx`、测试
- Modify: `frontend/src/app/(app)/agents/page.tsx`（按角色渲染）

**Interfaces:**
- Consumes: 现有 profiles/sessions/runs；`quality` 最新指标（Task 23 前返回 null）
- Produces: `GET /api/agent-center/catalog`（13 角色）、`GET /api/agent-center/sessions`；普通用户只读

- [ ] **Step 1: 写失败测试**

1. 13 角色顺序/映射（11 个 profile + 报告/知识无 profile 标注）；
2. 普通用户 GET 200、写 profile 403；
3. catalog 不返回密钥/提示词正文；
4. 空运行统计返回 0 而非假数据。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: FAIL。

- [ ] **Step 2: 实现后端**

只读 handler 使用现有 profile/session 服务；`quality` 端口可空；权限用 `RequireAdmin` 仅保护写/敏感字段。

- [ ] **Step 3: 前端**

`/agents` 管理员显示配置 + 运行观察 + 指标；普通用户只读卡片 + `ReadonlyBanner`；13 角色表格；测试。

- [ ] **Step 4: 运行测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...; cd ..\frontend; pnpm vitest run src/features/agent-console`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/agent frontend/src/features/agent-console frontend/src/app/\(app\)/agents
git commit -m "feat(p8): add agent center"
```

### Task 20: 设置基础配置 + 审计查看器 + 审计覆盖（并发=是；写作用域 `settings/**`、`audit/**`、各上下文写路径审计）

**Files:**
- Create: `frontend/src/features/settings/BasicConfigPanel.tsx`、`AuditLogPage.tsx`、测试
- Modify: `frontend/src/features/settings/SettingsTabs.tsx`、`frontend/src/app/(app)/settings/page.tsx`
- Modify: `backend/internal/audit/...`（筛选/导出所需字段）、各上下文写路径审计调用

**Interfaces:**
- Consumes: `GET /api/admin/audit`（既有）
- Produces: 设置页基础配置只读；审计列表筛选/详情/CSV 导出；资源/版本/脑图写操作审计

- [ ] **Step 1: 写失败测试**

1. 设置页出现“基础配置/审计日志”Tab（仅 admin）；
2. 基础配置不显示密钥；
3. 审计页筛选 actor/action/时间、分页、空态、错误；
4. 写操作（创建需求/脑图节点）产生 audit 记录，正文不含敏感字段。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/audit/...; cd ..\frontend; pnpm vitest run src/features/settings`
Expected: FAIL。

- [ ] **Step 2: 实现**

基础配置从 config/env 只读投影；审计页用现有 API；审计覆盖补 action（`resource.*`/`version.*`/`mindmap.*`），只记 ID/状态。

- [ ] **Step 3: 运行测试**

Run: 同上
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/audit backend/internal/*/application frontend/src/features/settings frontend/src/app/\(app\)/settings
git commit -m "feat(p8): add basic config and audit viewer"
```

### Task 21: 移动端主导航与响应式（并发=是；写作用域 `shell/{Sidebar,Topbar,AppShell}.tsx`）

**Files:**
- Modify: `frontend/src/features/shell/Sidebar.tsx`、`Topbar.tsx`、`AppShell.tsx`
- Create: `frontend/src/features/shell/MobileNav.tsx`、测试
- Modify: `frontend/playwright.config.ts`（如需 viewport projects）

**Interfaces:**
- Consumes: 既有导航数据 `navigation/`
- Produces: <1024 汉堡 + 抽屉导航；375/768/1280 可导航；焦点陷阱/Esc/遮罩关闭

- [ ] **Step 1: 写失败测试**

`MobileNav.test.tsx`：<lg 显示汉堡；打开抽屉焦点进入；Esc/遮罩关闭并归还焦点；路由切换自动关闭；`aria-expanded` 正确。

Run: `cd frontend; pnpm vitest run src/features/shell/MobileNav.test.tsx`
Expected: FAIL。

- [ ] **Step 2: 实现**

Sidebar 拆为桌面固定 + 移动抽屉；Topbar 汉堡；AppShell 控制状态；body scroll lock；44px 触控目标。

- [ ] **Step 3: 运行测试 + build**

Run: `cd frontend; pnpm vitest run src/features/shell; pnpm build`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add frontend/src/features/shell frontend/playwright.config.ts
git commit -m "feat(p8): add mobile navigation"
```

### Task 22: 可访问性 + 空/错/重连 + 证据前后对比（并发=是；写作用域 `components/ui/**`、`EvidenceViewer`）

**Files:**
- Create: `frontend/src/components/ui/Skeleton.tsx`、`ErrorState.tsx`
- Modify: `frontend/src/features/execution/components/EvidenceViewer.tsx`、`frontend/src/features/versions/VersionDetailPage.tsx`（`?evidence=&compare=`）
- Create: `frontend/src/lib/a11y.test.tsx`（axe 基础）、`EvidenceViewer.compare.test.tsx`
- Modify: `frontend/package.json`（devDependency `axe-core`）

**Interfaces:**
- Consumes: 既有 EvidenceViewer
- Produces: 对比查看器；空/错/骨架统一组件；axe 无 serious/critical

- [ ] **Step 1: 写失败测试**

1. EvidenceViewer `compareWith` 渲染并排/滑块，Esc 关闭、焦点归还；
2. `?evidence=&compare=` 深链打开；
3. 关键页面 axe 无 serious/critical；
4. SSE 断开组件显示“重连中”，不是空态。

Run: `cd frontend; pnpm vitest run src/components/ui src/features/execution/components/EvidenceViewer`
Expected: FAIL。

- [ ] **Step 2: 实现 + 运行**

Run: `cd frontend; pnpm vitest run src/components/ui src/features/execution; pnpm typecheck`
Expected: PASS。

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/ui frontend/src/features/execution frontend/src/features/versions frontend/src/lib frontend/package.json
git commit -m "feat(p8): improve a11y and evidence comparison"
```

## Wave 4：质量平台

### Task 23: 评测语料/runner/指标/基线/看板（并发=否，先冻结语料）

**Files:**
- Create: `backend/internal/db/migrations/quality_0001_eval.sql`
- Create: `backend/internal/quality/domain/eval.go`
- Create: `backend/internal/quality/application/{ports.go,eval_service.go,eval_service_test.go}`
- Create: `backend/internal/quality/infrastructure/postgres/eval_repository.go`
- Create: `backend/internal/quality/interfaces/httpapi/{handlers.go,routes.go,handlers_test.go}`
- Create: `eval/README.md`、`eval/corpus/{requirements.json,environment.json}`、`eval/ground-truth/{impact.json,classification.json,issues.json,supplement.json}`、`eval/baseline.json`
- Create: `scripts/eval.ps1`
- Create: `frontend/src/features/quality/QualityPage.tsx`、`QualitySummary.tsx`、测试
- Modify: `backend/internal/server/server.go`、`backend/cmd/eztest/main.go`、`scripts/acceptance.ps1`

**Interfaces:**
- Consumes: P3–P7 APIs；fake LLM 模式；fixture executor
- Produces: `eval_run/eval_metric` 表；`POST /api/quality/eval-runs`（admin）、`GET /api/quality/eval-runs[/{id}]`；`scripts/eval.ps1` 退出码；五项指标 JSON

- [ ] **Step 1: 写指标失败测试**

`eval_service_test.go`：给定 fake 数据，`design_coverage=0.8`、`impact_recall=0.75`、`classification_accuracy=1.0`、`issue_false_positive_rate=0.0`、`supplement_hit_rate=0.5`；回归规则：低于基线 0.05 失败；`threshold` 与 `passed` 正确写入。Run → FAIL。

- [ ] **Step 2: 实现 quality 上下文 + 迁移**

按规格 §4.2/§9.1；service 计算指标；repo 持久化；handler 管理端鉴权；server/main 接线。

- [ ] **Step 3: 建语料与 runner**

`eval/corpus` 固定需求/环境；ground truth 覆盖：受影响用例、注入失败分类、问题真值、补充缺口；`baseline.json` 初始值取确定性模式首次通过结果。`scripts/eval.ps1`：启动独立 API/fake LLM/fixture，跑完整链路，POST 结果，比较基线，输出 `.tmp/p8-eval/*.json`；`-RealLLM` gated。

- [ ] **Step 4: 写 runner 冒烟测试**

`scripts/eval.ps1 -DryRun` 断言语料/基线存在、DB URL 目标安全、工具链可用，不调用 LLM；加入 `acceptance.ps1 -StaticOnly` 检查。

- [ ] **Step 5: 实现看板 + 运行**

`/quality` admin 页面：最近 run、五项指标、阈值、通过状态、历史。Run：`scripts/eval.ps1 -DryRun` + 前端测试 → PASS。

- [ ] **Step 6: Commit**

```bash
git add backend/internal/quality backend/internal/db/migrations/quality_0001_eval.sql eval scripts/eval.ps1 scripts/acceptance.ps1 frontend/src/features/quality backend/internal/server/server.go backend/cmd/eztest/main.go
git commit -m "feat(p8): add evaluation corpus and runner"
```

### Task 24: 代码事实质量与人工修正（并发=是；写作用域 `codeintel/**`、`code-intel/**`）

**Files:**
- Create: `backend/internal/db/migrations/codeintel_0007_fact_review.sql`
- Create: `backend/internal/codeintel/application/fact_review_service.go`、测试
- Create: `backend/internal/codeintel/infrastructure/postgres/fact_review_repository.go`、测试
- Modify: `backend/internal/codeintel/interfaces/httpapi/{handlers,routes}.go`
- Modify: `frontend/src/features/code-intel/FactsPanel.tsx`、代码仓页质量区块、测试
- Modify: `eval/ground-truth/impact.json`（Task 23 已建，补 precision/recall 断言）

**Interfaces:**
- Consumes: `code_fact`/`code_anchor`/analysis stats
- Produces: `POST /api/code/repos/{repoId}/facts/{factId}/reviews`、`GET /api/code/repos/{repoId}/quality`

- [ ] **Step 1: 写失败测试**

1. review 创建/覆盖（同 repo/fact/commit 唯一）；2. 非本仓 fact 404；3. quality 返回证据通过率/无效事实/置信度分布/修正数/impact precision/recall；4. 普通用户可创建 review、审计记录；5. 前端显示“已人工修正”标记。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/codeintel/...; cd ..\frontend; pnpm vitest run src/features/code-intel`
Expected: FAIL。

- [ ] **Step 2: 实现 + 运行**

迁移/服务/路由/前端；impact 指标从最近评测 run 读取（无则 null）。Run 同上 → PASS。

- [ ] **Step 3: Commit**

```bash
git add backend/internal/db/migrations/codeintel_0007_fact_review.sql backend/internal/codeintel frontend/src/features/code-intel eval/ground-truth/impact.json
git commit -m "feat(p8): add code fact review and quality metrics"
```

### Task 25: 视觉断言（并发=是；写作用域 `mindmap`/`execution`/`executor`）

**Files:**
- Create: `backend/internal/db/migrations/mindmap_0003_visual.sql`、`execution_0003_visual_evidence.sql`
- Modify: `backend/internal/mindmap/domain/assertion.go`、`application/graph_service.go`、`application/materialize.go`、`application/agent_tool/{schemas,tools}.go`
- Create: `backend/internal/mindmap/application/visual_assertion_service.go`、测试
- Create: `backend/internal/mindmap/infrastructure/postgres/visual_assertion_repository.go`
- Modify: `backend/internal/mindmap/interfaces/httpapi/{handlers,routes}.go`
- Modify: `backend/internal/execution/application/case_runner.go`、`domain/classification.go`
- Modify: `backend/internal/execution/infrastructure/executor/client.go`（`CompareVisual`）
- Create: `executor/src/visual.mjs`、`executor/test/visual.test.mjs`；Modify `executor/src/server.mjs`、`executor/package.json`（`pixelmatch`/`pngjs`）
- Modify: `frontend/src/features/mindmap/CaseEditor.tsx`、`frontend/src/features/execution/components/EvidenceViewer.tsx`、测试

**Interfaces:**
- Consumes: `evidence` 存储；executor 协议
- Produces: `kind=visual`；`case_visual_assertion`；`PUT .../assertions/{aid}/visual`；`POST .../visual/baseline`；executor `visual.compare`；四态分类

- [ ] **Step 1: 写失败测试（后端/executor）**

1. `case_assertion`/`assertion_result` 接受 `visual`；2. 无基线 → `inconclusive(case, missing_visual_baseline)`；3. viewport/DPR 不符 → `inconclusive(environment, visual_environment_mismatch)`；4. diff > threshold → `failed(application, visual_mismatch)`；5. ≤ threshold → passed；6. executor 像素比对/区域裁剪/阈值；7. 非 admin 基线审批 403；8. `case.visual_baseline_approved` 事件 detail 只含 ID/状态，不含图片。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/mindmap/... ./internal/execution/...; cd ..\executor; pnpm test`
Expected: FAIL。

- [ ] **Step 2: 实现迁移/领域/执行/executor**

按规格 §9.3；约束名用实际 `case_assertion_kind_check`（mindmap_0003）、`assertion_result_kind_check`/`evidence_kind_check`/`ck_evidence_kind`（execution_0003）；视觉证据 kind `visual_baseline/visual_actual/visual_diff`；executor 用 `pixelmatch`/`pngjs`（MIT），请求大小上限。

- [ ] **Step 3: 前端编辑/审批/查看 + 评测场景**

CaseEditor 增加 visual 参数（selector/region/threshold）；admin 基线审批；EvidenceViewer 展示 actual/diff/并排；测试。在 `eval/corpus` 增加 visual 场景、`eval/ground-truth/visual.json` 增加期望分类，Task 23 runner 纳入该场景。

- [ ] **Step 4: 运行测试**

Run: 同 Step 1 + `cd frontend; pnpm vitest run src/features/mindmap src/features/execution`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add backend/internal/mindmap backend/internal/execution backend/internal/db/migrations executor frontend/src/features/mindmap frontend/src/features/execution
git commit -m "feat(p8): add visual assertions"
```

### Task 26: 知识沉淀（版本内）（并发=是；写作用域 `version/knowledge*`、`knowledge/**`、prompt 注入）

**Files:**
- Create: `backend/internal/db/migrations/version_0006_knowledge.sql`
- Create: `backend/internal/version/domain/knowledge.go`
- Create: `backend/internal/version/application/{knowledge_service.go,knowledge_service_test.go,ports.go}`
- Create: `backend/internal/version/infrastructure/postgres/knowledge_repository.go`
- Create: `backend/internal/version/interfaces/httpapi/handlers_knowledge.go`
- Modify: `backend/internal/version/application/clarification_service.go`、`backend/internal/mindmap/application/run_service.go`（注入已批准知识）
- Create: `frontend/src/features/knowledge/{KnowledgePage.tsx,KnowledgePanel.tsx,ReviewDrawer.tsx}`、测试
- Modify: `frontend/src/features/versions/OverviewTab.tsx`、`frontend/src/app/(app)/knowledge/page.tsx`、`shell/todos/**`（审核待办）

**Interfaces:**
- Consumes: 版本/需求/用例/问题 ID
- Produces: `knowledge_entry`；API 见规格 §11.1；注入 `knowledgeRefs` 记录

- [ ] **Step 1: 写失败测试（后端）**

1. draft→approved/rejected（admin）；2. 驳回必填原因；3. revision CAS；4. 注入只取 approved、≤5 条/≤4KiB，运行记录 `knowledgeRefs`；5. 非 admin 审批 403；6. 正文不入事件。

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/version/... ./internal/mindmap/...`
Expected: FAIL。

- [ ] **Step 2: 实现迁移/服务/注入/路由**

按规格 §9.4；Prompt 注入用既有 run service 的端口组合，不改变 P3/P5 既有提示词版本号，只追加“版本知识”段并记录引用。

- [ ] **Step 3: 前端 + 测试**

知识页、版本入口、审核抽屉、待办分组；运行 `pnpm vitest run src/features/knowledge src/features/versions` → PASS。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/version backend/internal/mindmap frontend/src/features/knowledge frontend/src/features/versions frontend/src/app/\(app\)/knowledge frontend/src/features/shell/todos
git commit -m "feat(p8): add version knowledge accumulation"
```

---

## Wave 5：非功能与最终交付

### Task 27: 全局限流 + SLO（并发=是；写作用域 `server/ratelimit*`、`scripts/slo.ps1`）

**Files:**
- Create: `backend/internal/server/ratelimit.go`、`ratelimit_test.go`
- Modify: `backend/internal/server/server.go`、`backend/internal/config/config.go`、`.env.example`、`README.md` 环境变量表
- Create: `scripts/slo.ps1`
- Create: `docs/acceptance/p8/slo.md`

**Interfaces:**
- Consumes: 认证中间件注入的用户 ID
- Produces: 429 `rate_limited` + `Retry-After`；`EZTEST_RATE_LIMIT_RPS/BURST/SSE_MAX`；SLO metrics JSON

- [ ] **Step 1: 写失败测试**

`ratelimit_test.go`：超过 burst 返回 429 + `Retry-After`；不同用户独立桶；`RPS=0` 关闭；SSE 超并发 429；健康检查不计。Run → FAIL。

- [ ] **Step 2: 实现中间件/配置**

令牌桶按用户（认证后）/IP（认证前）；SSE 单独计数；server 装配顺序在 auth 之后、业务之前；配置校验范围。

- [ ] **Step 3: SLO 脚本 + 运行**

`scripts/slo.ps1` 播 100 版本/50 需求/200 用例/10k 事件；测总览首屏、链路 P95、8 并发执行、changes API；阈值见规格 §1.5；输出报告；失败非零退出。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/server backend/internal/config .env.example README.md scripts/slo.ps1 docs/acceptance/p8/slo.md
git commit -m "feat(p8): add rate limiting and slo measurement"
```

### Task 28: GC/保留策略（并发=是；写作用域 `execution/evidence/**`、`admin/gc*`、设置页）

**Files:**
- Create: `backend/internal/db/migrations/execution_0004_evidence_purge.sql`
- Create: `backend/internal/execution/application/gc_service.go`、测试
- Create: `backend/internal/execution/infrastructure/postgres/gc_repository.go`
- Modify: `backend/internal/server/server.go`（admin `POST /api/admin/gc`）
- Modify: `frontend/src/features/settings/BasicConfigPanel.tsx`（dry-run 结果/执行）
- Create: `docs/operations.md`（GC 章节，Task 33 汇总）

**Interfaces:**
- Consumes: 证据/附件/导出/报告文件；引用表
- Produces: `POST /api/admin/gc` `{dryRun:true}` → `{candidates,bytes,protected}`；非 dry-run 仅 `EZTEST_GC_ENABLED=true`

- [ ] **Step 1: 写失败测试**

1. 未引用且超期 → 候选；2. 发布报告/未关闭问题/最近 N 批/基线证据 → 受保护；3. dry-run 不删文件；4. 非 dry-run 且 `GC_ENABLED=false` → 409；5. 删除写审计、保留元数据。Run → FAIL。

- [ ] **Step 2: 实现**

按规格 §10.2；引用保护 SQL；文件删除前 sha256 校验；失败不删；UI 二次确认。

- [ ] **Step 3: 运行测试 + 前端**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...; cd ..\frontend; pnpm vitest run src/features/settings`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/db/migrations/execution_0004_evidence_purge.sql backend/internal/execution backend/internal/server/server.go frontend/src/features/settings docs/operations.md
git commit -m "feat(p8): add evidence gc with reference protection"
```

### Task 29: 备份/恢复脚本与演练（并发=是；写作用域 `scripts/{backup,restore}.ps1`、`docs/operations.md`）

**Files:**
- Create: `scripts/backup.ps1`、`scripts/restore.ps1`、`scripts/test-backup.ps1`
- Create: `docs/acceptance/p8/backup-drill.md`
- Modify: `docs/operations.md`

**Interfaces:**
- Consumes: `EZTEST_DATABASE_URL`、`EZTEST_EVIDENCE_DIR`、`EZTEST_REPORT_DIR`、`EZTEST_EXPORT_DIR`、`EZTEST_VERSION_ATTACHMENT_DIR`、`EZTEST_STORAGE_DIR`
- Produces: 带 manifest/sha256 的备份目录；恢复校验报告；RPO/RTO 记录

- [ ] **Step 1: 写失败测试**

`scripts/test-backup.ps1`：备份到 `.tmp/p8-backup`，断言 manifest 含 DB dump/目录/哈希；恢复到新库/新目录后行数与 sha256 一致；拒绝非空目标。Run → FAIL。

- [ ] **Step 2: 实现脚本**

`pg_dump -Fc` + tar 目录 + manifest；不打包 `.env`；恢复校验 sha256；安全拒绝覆盖（`-Force` 二次确认）；记录耗时。

- [ ] **Step 3: 本地演练**

Run: `powershell.exe -File scripts/test-backup.ps1`
Expected: PASS；写 `backup-drill.md`（RPO 按需、RTO ≤30 分钟目标、密钥丢失说明）。

- [ ] **Step 4: Commit**

```bash
git add scripts/backup.ps1 scripts/restore.ps1 scripts/test-backup.ps1 docs/operations.md docs/acceptance/p8/backup-drill.md
git commit -m "feat(p8): add backup and restore drill"
```

### Task 30: 多实例单活（并发=是；写作用域 `platform/leader/**`、autonomy/scheduler 接线）

**Files:**
- Create: `backend/internal/platform/leader/leader.go`、`leader_test.go`
- Modify: `backend/internal/execution/application/autonomy_worker.go`、`scheduler.go`（tick 加锁）
- Modify: `backend/internal/config/config.go`（实例 ID/lock key）、`cmd/eztest/main.go`
- Create: `backend/internal/execution/integration/leader_integration_test.go`

**Interfaces:**
- Consumes: `pgxpool`；`EZTEST_INSTANCE_ID`/`EZTEST_LEADER_LOCK_KEY`
- Produces: `leader.Try(ctx, key) (release func(), ok bool, err error)`；同一 tick 仅一个实例执行

- [ ] **Step 1: 写失败测试**

`leader_test.go`：同 key 两连接，第二个 Try=false；释放后可得；不同 key 互不影响。集成测试：两个 worker 同时 tick，批次只被处理一次。Run → FAIL。

- [ ] **Step 2: 实现 + 接线**

`pg_try_advisory_lock`/`pg_advisory_unlock`；tick 内持有并在结束释放；未获锁 debug 日志；单实例行为不变；优雅退出释放。

- [ ] **Step 3: 运行测试**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/platform/leader; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/execution/integration`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add backend/internal/platform/leader backend/internal/execution/application backend/internal/execution/integration backend/internal/config backend/cmd/eztest/main.go
git commit -m "feat(p8): add multi-instance leader locks"
```

### Task 31: 执行器硬化 + P7 配置接线（并发=是；写作用域 `execution/**`、`executor/**`）

**Files:**
- Modify: `backend/internal/execution/application/scheduler.go`、`case_runner.go`（`MAX_TOOL_CALLS` 强制）
- Modify: `backend/internal/execution/infrastructure/executor/client.go`（超时收尾）
- Modify: `executor/src/{server.mjs,limits.mjs}`、`executor/test/limits.test.mjs`（WS 白名单/超时）
- Modify: `backend/internal/delivery/application/analysis_service.go`、`backend/internal/mindmap/application/graph_supplement.go`（读取 env 上限）
- Modify: `backend/internal/config/config.go`、`.env.example`、README 环境变量表

**Interfaces:**
- Consumes: `EZTEST_EXEC_MAX_TOOL_CALLS`、`EZTEST_ANALYSIS_MAX_CASES`、`EZTEST_SUPPLEMENT_MAX_CASES`
- Produces: 超限 `inconclusive(model, tool_call_budget_exhausted)`；超时 `inconclusive(model, executor_timeout)`；env 真正生效

- [ ] **Step 1: 写失败测试**

1. 工具调用超过上限 → 用例 `inconclusive(model)`；2. executor 超时后台任务被取消并落分类；3. WS 非白名单 host 拒绝；4. `ANALYSIS_MAX_CASES=3` 时分析只取 3；`SUPPLEMENT_MAX_CASES=2` 时补全只产 2。Run → FAIL。

- [ ] **Step 2: 实现 + 运行**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/execution/... ./internal/delivery/... ./internal/mindmap/...; cd ..\executor; pnpm test`
Expected: PASS。

- [ ] **Step 3: Commit**

```bash
git add backend/internal/execution backend/internal/delivery backend/internal/mindmap backend/internal/config executor .env.example README.md
git commit -m "fix(p8): enforce executor budgets and config wiring"
```

### Task 32: 安全审计脚本 + 安全头 + 权限矩阵（并发=是；写作用域 `scripts/security-audit.ps1`、Next headers）

**Files:**
- Create: `scripts/security-audit.ps1`
- Modify: `frontend/next.config.*`（security headers；CSP report-only）
- Create: `docs/acceptance/p8/security-audit.md`

**Interfaces:**
- Consumes: 仓库/依赖/权限端点
- Produces: 秘密扫描、`govulncheck`、`pnpm audit`、权限矩阵检查、许可清单；安全头

- [ ] **Step 1: 写失败检查**

脚本先断言当前缺少 headers 时失败（或输出未配置）；权限矩阵：普通用户对 admin-only 端点 403、只读端点 200。Run → FAIL。

- [ ] **Step 2: 实现**

秘密扫描排除 `.env/.tools/.tmp`；工具不存在记 SKIP 并说明；输出 Pass/Fail 表；headers 用 Next `headers()`；CSP `Report-Only`。

- [ ] **Step 3: 运行脚本 + build**

Run: `powershell.exe -File scripts/security-audit.ps1; cd frontend; pnpm build`
Expected: PASS/明确 SKIP；无 FAIL。

- [ ] **Step 4: Commit**

```bash
git add scripts/security-audit.ps1 frontend/next.config.* docs/acceptance/p8/security-audit.md
git commit -m "chore(p8): add security audit and headers"
```

### Task 33: 文档 + Compose（并发=是；写作用域 `docs/**`、`deploy/**`、README）

**Files:**
- Create: `docs/deployment.md`、`docs/operations.md`（汇总）、`docs/user-guide.md`、`docs/development.md`
- Modify: `deploy/docker-compose.yml`、`deploy/Dockerfile.api`、`deploy/README.md`、`README.md`、`docs/openapi/api.yaml`
- Modify: `docs/design/{implementation-phases,system-blueprint,frontend-design}.md`（P8 实现注记/D53–D69）

**Interfaces:**
- Consumes: 前 32 个任务的最终 API/env/行为
- Produces: 四类文档；Compose 卷（evidence/reports/exports）+ 代码仓只读挂载示例；OpenAPI 同步；设计文档实现注记

- [ ] **Step 1: 写文档完整性检查**

Create `scripts/check-docs.ps1`：断言四类文档存在、README 含 P8、OpenAPI 含新 path、Compose 含 `EZTEST_EVIDENCE_DIR/REPORT_DIR/EXPORT_DIR` 卷。Run → FAIL。

- [ ] **Step 2: 实现文档/Compose/OpenAPI**

按规格 §17；Docker 不可用则明确“仅静态校验、未实跑”；代码仓只读挂载注释示例；Dockerfile 创建并 chown 目录；**同步 `.env.example` 与 README 环境变量表（P8 新增变量）与 OpenAPI 新端点/错误/事件**。

- [ ] **Step 3: 运行检查 + 静态验收**

Run: `powershell.exe -File scripts/check-docs.ps1; powershell.exe -File scripts/acceptance.ps1 -StaticOnly`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add docs deploy README.md scripts/check-docs.ps1
git commit -m "docs(p8): add deployment operations and user docs"
```

### Task 34: 跨波 E2E 与回归（并发=否；写作用域 `frontend/e2e/p8-*.spec.ts`）

**Files:**
- Create: `frontend/e2e/p8-shell.spec.ts`、`p8-version-detail.spec.ts`、`p8-management.spec.ts`、`p8-quality.spec.ts`
- Modify: `scripts/e2e.ps1`（加入新 spec）

**Interfaces:**
- Consumes: Task 14–33 的最终 UI/API
- Produces: 覆盖 W2–W4 功能的浏览器回归；截图到 `docs/acceptance/p8/e2e/`

- [ ] **Step 1: 写失败 E2E**

覆盖：
1. 版本切换器保留 Tab/批次；待办面板分组/深链；全局搜索分组/Enter 打开/快捷键。
2. 脑图 Tab 与全屏路由一致；代码与变更批次时间线/受影响用例；报告 revision 切换/stale。
3. Agent 中心普通用户只读、admin 可配；审计页筛选；375/768 视口导航。
4. 知识草稿→admin 审批→`/knowledge` 可见；视觉断言基线审批→执行→差异证据；`/quality` admin 指标可见。

Run: `powershell.exe -File scripts/e2e.ps1 p8-shell p8-version-detail p8-management p8-quality`
Expected: FAIL（spec 未注册/元素缺失）。

- [ ] **Step 2: 实现 fixture/断言并注册**

复用 `scripts/e2e.ps1` 的 fake LLM/fixture/executor 与 admin 登录；所有断言含 `expectNoConsoleErrors` 与 `expectNoSecretInPage`；截图落盘。

- [ ] **Step 3: 运行并保存证据**

Run: 同上
Expected: 全部 passed。

- [ ] **Step 4: Commit**

```bash
git add frontend/e2e/p8-*.spec.ts scripts/e2e.ps1 docs/acceptance/p8/e2e
git commit -m "test(p8): add cross-wave e2e regression"
```

### Task 35: 最终真实验收（并发=否，串行）

**Files:**
- Create: `docs/acceptance/p8/acceptance.md`
- Create: `docs/acceptance/p8/**`（截图/日志索引）
- Create: `scripts/acceptance-p8-llm.ps1`（gated）

**Interfaces:**
- Consumes: Task 1–33
- Produces: P8 验收记录与证据索引；PASS/PASS WITH LIMITS/FAIL 判定

- [ ] **Step 1: 全量门禁**

Run:
```powershell
powershell.exe -File scripts/test.ps1
powershell.exe -File scripts/acceptance.ps1 -StaticOnly
powershell.exe -File scripts/acceptance.ps1
powershell.exe -File scripts/e2e.ps1
powershell.exe -File scripts/eval.ps1
```
Expected: 全部 EXIT=0；记录日志到 `.tmp/p8-final-*`。

- [ ] **Step 2: 真实 LLM + 真实浏览器**

Run gated：`$env:EZTEST_RUN_REAL_LLM="1"; powershell.exe -File scripts/acceptance-p8-llm.ps1`；启动 API/Web/executor，真实 Chrome 全流程（版本/需求/脑图/执行/链路/问题/报告/搜索/待办/知识/视觉断言），截图到 `docs/acceptance/p8/`。

- [ ] **Step 3: SLO/备份/安全**

Run `scripts/slo.ps1`、`scripts/test-backup.ps1`、`scripts/security-audit.ps1`；记录结果。

- [ ] **Step 4: 写验收记录**

`acceptance.md`：每波 DoD 证据、缺陷 #1 复验、已知限制（Docker/CI/真实 LLM 抖动）、判定；不夸大。

- [ ] **Step 5: Commit**

```bash
git add docs/acceptance/p8 scripts/acceptance-p8-llm.ps1
git commit -m "test(p8): record final acceptance"
```

---

## Wave Gates（Lead 执行，未通过不进入下一波）

| 波次 | 任务 | 门禁命令/动作 | 证据 |
|---|---|---|---|
| W0 | 1–4 | `scripts/test.ps1`、`acceptance.ps1 -StaticOnly`、`e2e.ps1`；重建产物后真实浏览器打开链路 | `.tmp/p8-w0-gate.log`、`docs/acceptance/p8/wave0/` |
| W1 | 5–13 | 链路单测/E2E/axe/SLO-chain；真实 LLM 版本截图；>100 截断 fixture | `docs/acceptance/p8/wave1/` |
| W2 | 14–18 | 真实浏览器：版本切换/Todo/搜索/脑图 Tab/代码变更/报告 revision；深链刷新 | `docs/acceptance/p8/wave2/` |
| W3 | 19–22 | 普通用户只读/管理员写；375/768/1280 导航；axe；审计筛选 | `docs/acceptance/p8/wave3/` |
| W4 | 23–26 | `scripts/eval.ps1` 确定性门禁；视觉断言真实 executor；知识审核→注入 | `docs/acceptance/p8/wave4/`、`eval/baseline.json` |
| W5 | 27–35 | `test.ps1`、`acceptance.ps1`、`e2e.ps1`、`eval.ps1`、`slo.ps1`、备份演练、安全审计、真实 LLM + Chrome | `docs/acceptance/p8/acceptance.md` |

并发任务完成顺序：同一波内标 `并发=是` 的任务由不同子代理并行；Lead 在每个写作用域完成后按依赖顺序合并验收。若任务失败，回退该任务提交、修复后重跑该波门禁；不得带失败进入下一波。

---

## Self-Review

**1. Spec coverage（规格 → 任务）**
- §5 探索链路 v2：Task 5–13；C1–C10 分散在 Task 6–12。
- §6 全局壳：Task 14（版本切换/待办）、Task 15（搜索）。
- §7 版本详情：Task 16（脑图 Tab）、Task 17（代码变更）、Task 18（报告）。
- §8 管理面/体验：Task 19（Agent 中心）、Task 20（设置/审计）、Task 21（移动）、Task 22（a11y/证据对比）。
- §9 质量平台：Task 23（评测）、Task 24（代码质量）、Task 25（视觉断言）、Task 26（知识）。
- §10 非功能：Task 27（限流/SLO）、Task 28（GC）、Task 29（备份）、Task 30（leader）、Task 31（执行器/配置）、Task 32（安全）、Task 33（文档）。
- §1.5 DoD：Task 35 汇总；Task 34 覆盖跨波 E2E；每波门禁见各 Task 与 Wave Gates。
- P9：已在规格/implementation-phases 登记，本计划不实现。

**2. Step scan**
- 每个 Task 均有失败测试 → 实现 → 通过 → commit；无“适当处理/TBD”步骤。
- 大任务（Task 6/25/26）已按子能力拆测试，但保留为单 Task 以便并发；实施时可再拆子步骤，写作用域不变。

**3. Type consistency**
- `chain.Query/Snapshot/NodeDTO/EdgeDTO/CountsDTO/TruncatedDTO` 在 Task 6 定义，Task 7/8/9/10 引用一致。
- `SearchHit`、`TodoItem`、`EvalRun/EvalMetric`、`KnowledgeEntry` 在各自 Task 定义；前端 types 同名 camelCase。
- `AssertionKindVisual = "visual"` 与迁移 CHECK、executor 协议一致。
- `leader.Try(ctx,key) (release func(), ok bool, err error)` 在 Task 30 定义并被 worker 使用。

**4. Review Focus → 测试**
1. 截断/latest attempt：Task 6 Step 1、Task 7 Step 1、Task 13。
2. SSE 断线：Task 8 Step 1、Task 10/11、Task 13。
3. React Flow SSR：Task 5 Step 2/4、Task 10 Step 4。
4. 权限：Task 19/20/25/26/28/32。
5. 降级/失败安全：Task 8/11/23/25。

**5. Proportion**
- 计划约 35 个任务，每任务 3–7 步；测试断言与接口为主，未内联大段实现代码。规格与计划互为引用，实施细节由任务内测试钉住。

**已知边界（写入 Task 34 验收记录）：** Docker/Compose 仅静态校验；CI 不托管运行；真实 LLM 指标为 gated 报告；SLO 为本地口径；视觉断言仅 Chromium PNG。

---

## Execution Handoff

计划已保存。实现方式：用户已要求“可并发任务并发执行、繁重调查交给子代理”，因此采用 **subagent-driven-development**：每个任务由独立子代理实施，主会话按写作用域调度并发；每波结束由 Lead 做真实浏览器验收并提交证据。若用户要求改为 native 顺序执行，则按 executing-plans 逐任务执行。

**请审阅计划：是否准确覆盖你想要的 P8 交付？确认后我按 W0→W5 开始并发实现与逐波验收。**


