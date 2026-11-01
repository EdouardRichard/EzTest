# EzTest 0.2 P6 全功能补齐 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `feat/v0.2-p6` 上交付 P6 全功能补齐：质量/评测、全局搜索、待办中心、Agent 中心与提示词运行态定制、全局问题/报告中心、探索链路 v2、能力对齐矩阵，并以真实 LLM + Chrome 验收。

**Architecture:** 契约先行；新增 `quality`/`search`/`workspace` 三个四层上下文，扩展 `agent`/`delivery`/`chain`；前端新增 quality/search/agent-center feature 并扩展 shell/delivery/chain；迁移 #32/#33、设置 69→72；6 条写作用域互斥的并行流 + Lead 收口共享文件；验收全程真实 LLM，不使用 fake-LLM。

**Tech Stack:** Go 1.24 + PostgreSQL 16 + Next.js/React + TypeScript + Vitest + Playwright + Node/Playwright executor + DeepSeek `deepseek-flash`（真实 LLM）。

**Spec:** `docs/superpowers/specs/2026-11-01-eztest-v02-p6-parity-quality-search-agent-center-design.md`

## Global Constraints

- 分支：`feat/v0.2-p6`；完成后合回 `feat/v0.2`；不自动进入 P7。
- **不改 `docs/version-V0.2/` 设计文档**；只更新 contracts、handoff、迁移与测试/验收证据。
- 契约只追加；P0–P5 `frozen` 语义不变；P6 冻结 `0.2.0-p6` / `x-frozen: P6`。
- 迁移只追加：`quality_0001_eval.sql` #32、`agent_0005_profile_prompt_revision.sql` #33；不改历史迁移；`order.txt` 33 条。
- 设置 69→72：`search.max_results_per_type=5`、`todos.max_items=50`、`quality.eval_baseline_regression=0.05`；不新增 env。
- 不新增事件/task type/profile/tool/executor 协议；不改 P5 `visual-metrics` 语义。
- 跨上下文只走对方 application 端口；禁止直接 SQL join 他域表；搜索/待办/链路只读、不写业务表。
- 权限：admin 写 profile/评测；登录读目录/运行/质量/搜索/待办/全局中心；普通用户**永不返回 prompt/transcript**；评测样本仅 admin。
- **验收/评测/E2E/演示全程真实 LLM**；不使用 fake-LLM；单测/集成测试的离线替身只验证逻辑，不产出验收结论；真实 LLM 评测阈值报告轨，不阻断阶段。
- 真实 LLM 失败先按系统缺陷定位并修复后重跑；不得以 fake 代替或把行为失败标“模型不稳定”跳过。
- 用户脏文件不提交：`docs/development.md`、`docs/startup.md`、`docs/用户专用文件，禁止AI阅读.md`、`scripts/dev-all.ps1`。
- 每任务 TDD：先写失败测试 → 验证失败 → 最小实现 → 验证通过 → 小步提交。
- 真实 LLM 环境隔离：不碰开发栈（13333/6868/13336/5433）。

## Review Focus

1. **真实 LLM 验收稳定性**：无 fake 兜底，超时/重试/最终器必须让 S0–S9 可重复；失败按系统缺陷定位。
2. **提示词模板编辑边界**：未知变量、缺失 `[profile:<key>]`、超长、渲染失败回退默认；保存/预览/运行期三处一致。
3. **跨上下文读模型性能与空数据**：搜索/待办/质量/全局中心在空库、部分 Provider 失败、大数据量下不得 N+1/500/假 0。
4. **权限与脱敏**：普通用户不得看到 prompt/transcript/评测样本；admin-only 组不得泄露；深链不得越权。
5. **迁移/契约门禁漂移**：31→33 迁移、ops/events/设置计数、P2–P5 门禁前向兼容；P5 冻结面只增不改。

---

## File Structure（先锁定边界）

```text
backend/internal/
├─ quality/{domain,application,infrastructure,interfaces/httpapi}/
├─ search/{domain,application,infrastructure,interfaces/httpapi}/
├─ workspace/{domain,application,infrastructure,interfaces/httpapi}/
├─ agent/
│  ├─ domain/prompt.go
│  ├─ application/{prompt_catalog,prompt_service,profile_admin,agent_center}.go
│  ├─ infrastructure/postgres/{prompt_repo,profile_admin,agent_center}.go
│  └─ interfaces/httpapi/{profile_handler,agent_center_handler,dto,routes}.go
├─ delivery/{application/global.go,infrastructure/postgres/global.go,interfaces/httpapi/global_handler.go}
├─ chain/{domain,application,infrastructure/providers,interfaces/httpapi}/（增量）
├─ server/server.go（P6 deps/routes）
└─ db/migrations/{quality_0001_eval,agent_0005_profile_prompt_revision}.sql

frontend/src/
├─ features/quality/{QualityPage,api,types}.tsx/ts
├─ features/search/{CommandPalette,useGlobalSearch,api,types}
├─ features/shell/{Topbar,TodoDrawer}（增量）
├─ features/agent-center/{AgentCenterPage,ProfileEditor,PromptEditor,PromptVersionList,api,types}
├─ features/delivery/{GlobalIssuesPage,GlobalReportsPage,global-api}
├─ features/chain/（增量）
├─ app/(app)/{quality,agents,issues,reports}/page.tsx
└─ navigation/nav-items.ts

scripts/
├─ p6-contract-check.cjs
├─ p6-acceptance.ps1
├─ p6-e2e.ps1
├─ eval.ps1（0.2 重写）
└─ eval/v02/**（语料/真值/baseline）

docs/version-V0.2/
├─ contracts/{openapi.yaml,db-schema.md,migrations-order.md,settings.md,agent-profiles-tools.md,README.md}
├─ acceptance/p6/{acceptance.md,capability-parity.md,artex-prompt-parity.md,screenshots/}
└─ phases/phase-06-handoff.md
```

---

## Wave 0 — 契约、迁移、共享骨架（Lead 串行）

### Task 1: P6 数据迁移与设置键

**Files:**
- Create: `backend/internal/db/migrations/quality_0001_eval.sql`
- Create: `backend/internal/db/migrations/agent_0005_profile_prompt_revision.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/settings/application/catalog.go`
- Modify: `backend/internal/db/migrate_test.go`
- Test: `backend/internal/db/migrate_integration_test.go`（既有，增量）

**Interfaces:**
- Consumes: 既有迁移执行器/`schema_migrations`；`settingsctl` catalog 结构。
- Produces: 表 `eval_run`、`eval_metric`、`agent_profile_prompt_revision`；设置键 3 个；迁移 order #32/#33；迁移计数 33。

- [ ] **Step 1: 写失败测试（迁移计数/表存在/设置键）**

在 `migrate_test.go` 增加断言：`wantMigrations = 33`、tail 为 `agent_0005_profile_prompt_revision.sql`；在 `migrate_integration_test.go` 增加 `SELECT to_regclass('eval_run')`、`eval_metric`、`agent_profile_prompt_revision` 非空；在 settings catalog 测试断言 72 键含三个新键及默认值。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/db ./internal/settings/...`
Expected: FAIL（迁移数/表/键缺失）。

- [ ] **Step 3: 实现迁移与设置键**

按 spec §3.2/§3.3 原样写入两个 SQL；`order.txt` 追加两行；settings catalog 增加 `search.max_results_per_type`(5,1–20)、`todos.max_items`(50,1–200)、`quality.eval_baseline_regression`(0.05,0–1)，分类 search/workspace/quality、非 secret、热加载。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/db ./internal/settings/...`
Expected: PASS；迁移两次启动第二次 0 应用（既有幂等测试）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/migrations backend/internal/settings/application/catalog.go backend/internal/db/*_test.go
git commit -m "feat(p6): add quality/prompt migrations and settings keys"
```

---

### Task 2: P6 契约增量（openapi/db-schema/migrations/settings/agent-profiles-tools/README）

**Files:**
- Modify: `docs/version-V0.2/contracts/openapi.yaml`
- Modify: `docs/version-V0.2/contracts/db-schema.md`
- Modify: `docs/version-V0.2/contracts/migrations-order.md`
- Modify: `docs/version-V0.2/contracts/settings.md`
- Modify: `docs/version-V0.2/contracts/agent-profiles-tools.md`
- Modify: `docs/version-V0.2/contracts/README.md`
- Test: `scripts/p6-contract-check.cjs`（Task 21 创建；本任务先写 needle 列表）

**Interfaces:**
- Consumes: spec §4/§5/§8/§14 的路径/DTO/语义。
- Produces: `0.2.0-p6` OpenAPI（20 新 operation + chain v2 增量）；P6 契约冻结语义（本任务可先标 draft，Task 21 翻 frozen）。

- [ ] **Step 1: 写契约检查的失败 needle**

在 `scripts/p6-contract-check.cjs` 写入最小检查：OpenAPI `info.version === '0.2.0-p6'`、20 个新 path 存在、chain 新参数存在、db-schema 含三表、migrations 含 #32/#33、settings 72 键、`agent-profiles-tools.md` 含 prompt 版本/变量/下一 run 语义。运行应 FAIL。

- [ ] **Step 2: 运行确认失败**

Run: `node scripts/p6-contract-check.cjs`
Expected: FAIL（契约未写入）。

- [ ] **Step 3: 写入契约增量**

按 spec §4.2/§4.3 写 openapi paths/schemas/tags、chain 参数/字段；`db-schema.md` §14/§15；`migrations-order.md` §8；`settings.md` §4f；`agent-profiles-tools.md` §9；`contracts/README.md` P6 状态/计数。字段 snake_case、ID 字符串、时间 RFC3339、错误信封复用。

- [ ] **Step 4: 运行确认通过**

Run: `node scripts/p6-contract-check.cjs`
Expected: PASS（本任务检查项）。

- [ ] **Step 5: 提交**

```bash
git add docs/version-V0.2/contracts scripts/p6-contract-check.cjs
git commit -m "docs(p6): freeze phase contracts"
```

---

### Task 3: 契约门禁前向兼容 + P6 deps/routes 骨架 + 导航/Topbar 接线点

**Files:**
- Modify: `scripts/p2-contract-check.cjs`、`p3-contract-check.cjs`、`p4-contract-check.cjs`、`p5-contract-check.cjs`
- Modify: `backend/internal/server/server.go`
- Modify: `backend/cmd/eztest/main.go`
- Modify: `frontend/src/navigation/nav-items.ts`
- Modify: `frontend/src/features/shell/Topbar.tsx`
- Test: `frontend/src/features/shell/Topbar.test.tsx`

**Interfaces:**
- Consumes: Task 1/2。
- Produces: `server.Deps` 新增 P6 handler 组字段（nil 不挂载）；`main.go` 预留构造点；nav placeholder 清理；Topbar 搜索/待办插槽；P2–P5 gate 只验增量。

- [ ] **Step 1: 写失败测试**

`Topbar.test.tsx` 断言搜索入口与待办按钮在无版本时可见；nav 测试断言 issues/reports/quality/agents 不再标 placeholder、knowledge 仍标 placeholder。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/shell/Topbar.test.tsx src/navigation`
Expected: FAIL。

- [ ] **Step 3: 实现骨架/接线**

`server.Deps` 增加可选 P6 handler 组（nil 不挂载）；`main.go` 预留构造点（返回 nil 时保持 P0–P5）；nav 移除 issues/reports/quality/agents 的 placeholder；Topbar 增加搜索/待办插槽 props（未接实现时禁用）。

- [ ] **Step 4: 运行确认通过 + 全量基线**

Run: `cd backend; ..\.tools\go\bin\go.exe build ./...`；`cd frontend; pnpm typecheck; pnpm vitest run src/features/shell src/navigation`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/server backend/cmd/eztest frontend/src/navigation frontend/src/features/shell scripts/p*-contract-check.cjs
git commit -m "feat(p6): add context skeletons, nav and forward-compatible gates"
```

---

### Task 3b: 跨上下文只读查询端口（Lead 串行）

> 并行流不得直接修改他域 application 包；本任务一次性补齐所有 P6 只读查询方法，后续 Task 6/9/11/13/18 只消费。

**Files:**
- Modify: `backend/internal/version/application/*`、`backend/internal/design/application/*`、`backend/internal/execution/application/*`、`backend/internal/delivery/application/*`、`backend/internal/agent/application/*`、`backend/internal/eventing/application/*`
- Test: 各 owning 包 `*_test.go`

**Interfaces:**
- Consumes: 既有各上下文 service/repository。
- Produces（方法名/语义固定；签名按现有 service 类型适配）：
```go
// version
CountRequirementClosure(ctx, versionID *int64) (num, denom int, err error)
SearchVersions(ctx, q string, limit int) ([]SearchHit, error)
SearchRequirements(ctx, q string, limit int) ([]SearchHit, error)
VersionTitles(ctx, ids []int64) (map[int64]string, error)
// design
CountCaseConfirmation(ctx, versionID *int64) (num, denom int, err error)
SearchCases(ctx, q string, limit int) ([]SearchHit, error)
// execution
CountExecutionPass(ctx, versionID *int64) (num, denom int, err error)
CountEvidenceCompleteness(ctx, versionID *int64) (num, denom int, err error)
OpenHumanRequests(ctx, viewer platform.Actor, limit int) ([]HumanRequestHit, error)
DeadWorkItems(ctx, limit int) ([]WorkItemHit, error)
// delivery
CountIssueValidity(ctx, versionID *int64) (num, denom int, err error)
SearchIssues(ctx, q string, limit int) ([]SearchHit, error)
SearchReports(ctx, q string, limit int) ([]SearchHit, error)
// agent
CountTaskRetries(ctx, versionID *int64) (num, denom int, err error)
// eventing
CountEvents(ctx, eventType string, channel string) (int, error)
```
- `SearchHit{ID,Title,Subtitle,VersionID,VersionTitle,Status,URL}` 由 owning 包定义或共享 platform 类型；url 在 handler 层补全更稳妥时由 handler 组装。

- [ ] **Step 1: 写失败测试**

各 owning 包：空库返回 0/空；版本过滤；q/limit；权限（OpenHumanRequests 的 admin 过滤）；SearchHit 字段；计数分母 0。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/{version,design,execution,delivery,agent,eventing}/...`
Expected: FAIL（方法缺失）。

- [ ] **Step 3: 实现只读方法**

不新增写路径；不跨上下文 SQL；批量查询避免 N+1；版本标题批量；事件计数走 eventing 端口。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/{version,design,execution,delivery,agent,eventing}/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/{version,design,execution,delivery,agent,eventing}/application
git commit -m "feat(p6): add cross-context read query ports"
```

---

## Wave 1 — 六条并行能力流

> 以下 Task 4–21 在 Task 3b 完成后并行；写作用域互斥；共享文件（server.go/main.go/contracts/nav/Topbar/routes）只由 Lead 在 Task 22 收口。


### Task 4: quality 领域模型与指标派生

**Files:**
- Create: `backend/internal/quality/domain/eval.go`
- Create: `backend/internal/quality/domain/eval_test.go`
- Create: `backend/internal/quality/domain/overview.go`（运营指标 DTO/口径常量）
- Test: `backend/internal/quality/domain/eval_test.go`

**Interfaces:**
- Consumes: 无（纯 domain）。
- Produces:
```go
type Sample struct{ ConfirmedRequirements, CoveredRequirements, ImpactExpected, ImpactHit, ImpactSelected, InjectedFailures, CorrectClassifications, AIIssues, TruePositiveIssues, SupplementCases, SupplementHits int; VisualInjected *int; VisualCorrect *int }
type MetricDetail struct{ Numerator, Denominator int; ThresholdPassed, Regressed bool; Baseline *float64; Reason string; ZeroDenominator bool }
type Metric struct{ Name string; Value, Threshold float64; Passed bool; Detail MetricDetail }
func ValidateSample(Sample) error
func Evaluate(Sample, map[string]float64, float64) (string, []Metric)
```
- 阈值常量按 spec §5.3 real_llm：design 0.80、impact recall 0.70、precision 0.60、classification 0.80、FP 0.15、supplement 0.50、visual 0.80。

- [ ] **Step 1: 写失败测试**

覆盖：11 计数物理约束（如 covered>confirmed 报错）；6+1 公式；分母 0；FP 方向；baseline 回归标记；status passed/failed；reason 枚举；visual 可选缺失。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/domain`
Expected: FAIL。

- [ ] **Step 3: 实现域逻辑**

纯函数、无 I/O；epsilon 1e-9；`passed=thresholdPassed&&!regressed`；status 归并。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/domain`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/quality/domain
git commit -m "feat(p6): add quality eval domain"
```

---

### Task 5: quality 持久化/API/overview

**Files:**
- Create: `backend/internal/quality/application/{service,ports}.go`
- Create: `backend/internal/quality/infrastructure/postgres/repository.go`
- Create: `backend/internal/quality/interfaces/httpapi/{routes,handler,dto}.go`
- Create: `backend/internal/quality/application/service_test.go`
- Create: `backend/internal/quality/interfaces/httpapi/handler_test.go`
- Create: `backend/internal/quality/infrastructure/postgres/integration_test.go`
- Test: 同上

**Interfaces:**
- Consumes: Task 4 domain；`platform/id.Generator`、`platform/httpx`、`auditapp.Service`、`settingsapp.Service`。
- Produces:
```go
type Repository interface{ Insert(ctx, EvalRun, []Metric) error; Get(ctx,int64)(EvalRun,error); List(ctx,int)([]EvalRun,error); Latest(ctx)(*EvalRun,error) }
type OperationalSource interface{ Snapshot(ctx context.Context, versionID *int64) (OperationalSnapshot, error) }
func NewService(Repository, id.Generator, clock, OperationalSource, settings, audit) *Service
func (s *Service) List(ctx, limit int) ([]EvalRun, error)
func (s *Service) Record(ctx, platform.Actor, RecordInput) (EvalRun, error)
func (s *Service) Get(ctx, id int64) (EvalRun, error)
func (s *Service) Overview(ctx, *int64) (Overview, error)
```
- HTTP：`GET/POST /api/quality/eval-runs`、`GET /api/quality/eval-runs/{id}`（admin）、`GET /api/quality/overview`（登录）。

- [ ] **Step 1: 写失败测试**

unit：Record 拒绝非法 sample、忽略客户端 passed、写 6+1 指标、审计 metadata 无正文；List limit 1–50；Get 404；Overview 7 指标 available/分母 0。
handler：admin 403、201/200、400 details.field、401。
integration：插入/查询/排序/权限代理（用既有 testutil）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/...`
Expected: FAIL。

- [ ] **Step 3: 实现 repo/service/handlers**

单事务 insert run+metrics；`status=error` 允许无 sample；服务端派生；audit `quality.eval_recorded`（metadata run_id/status/metrics 计数）；overview 用 `OperationalSource`（Task 6 提供，本任务先返回 available=false 或空实现以便编译）。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/quality
git commit -m "feat(p6): add quality eval persistence and API"
```

---

### Task 6: 运营指标聚合与跨上下文端口

**Files:**
- Create: `backend/internal/quality/infrastructure/metrics/aggregator.go`
- Create: `backend/internal/quality/infrastructure/metrics/aggregator_test.go`
- Test: 同上

**Interfaces:**
- Consumes: Task 3b 的只读查询端口（`CountRequirementClosure`/`CountCaseConfirmation`/`CountExecutionPass`/`CountIssueValidity`/`CountEvidenceCompleteness`/`CountEvents`/`CountTaskRetries`）；spec §5.2 口径。
- Produces:
```go
type OperationalSnapshot struct{ Metrics []OperationalMetric }
type Aggregator struct{ Version, Design, Execution, Delivery, Agent, Events ports }
func (a *Aggregator) Snapshot(ctx, *int64) (OperationalSnapshot, error)
```
- 每项 `available=false` 当分母 0；`context_compaction_count` 读事件计数；`task_failure_retry_rate` 读 work_item.attempts。

- [ ] **Step 1: 写失败测试**

fake 端口返回固定计数，断言 7 指标 numerator/denominator/value/available；分母 0 时 null；版本过滤生效。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/infrastructure/metrics`
Expected: FAIL。

- [ ] **Step 3: 实现聚合器**

只消费 Task 3b 端口，不直接 SQL join 他域表，不修改他域包。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/quality/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/quality/infrastructure/metrics
git commit -m "feat(p6): aggregate quality operational metrics"
```

---

### Task 7: 质量中心前端

**Files:**
- Create: `frontend/src/features/quality/{QualityPage.tsx,api.ts,types.ts,QualityPage.test.tsx}`
- Modify: `frontend/src/app/(app)/quality/page.tsx`
- Test: `frontend/src/features/quality/QualityPage.test.tsx`

**Interfaces:**
- Consumes: `GET /api/quality/overview`、`/api/quality/visual-metrics`、`/api/quality/eval-runs`（admin）；`api-client`；`useIsAdmin`。
- Produces: `/quality` 页面（7 运营指标 + P5 视觉区 + admin 评测看板/阈值/基线/历史/空错载/拒绝态）。

- [ ] **Step 1: 写失败测试**

mock API：普通用户看到运营+视觉、无评测请求；admin 看到 run 列表/指标/基线；空态“尚未运行评测”；错误重试；`available=false` 显示“不可用”不是 0。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/quality/QualityPage.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现页面/api/types**

复用 `QualityMetricsPanel`；admin 才请求 eval-runs；table 横向滚动；testid `quality-eval-*`；不显示 0 假数据。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/quality`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/quality "frontend/src/app/(app)/quality/page.tsx"
git commit -m "feat(p6): add quality centre page"
```

---

### Task 8: 0.2 评测语料与真实 LLM runner

**Files:**
- Create: `eval/v02/{README.md,baseline.json,corpus/*,ground-truth/*}`
- Rewrite: `scripts/eval.ps1`
- Create: `.tmp/p6-eval/`（运行时）
- Test: `scripts/eval.ps1 -DryRun` + 真实 LLM 集成（验收 Task 22 调用）

**Interfaces:**
- Consumes: Task 5 API、P6 验收真实 LLM 数据/流程、admin bearer。
- Produces: `scripts/eval.ps1 -Mode real-llm|-DryRun`；POST `mode=real_llm`；`.tmp/p6-eval/{result,observation}.json`。

- [ ] **Step 1: 写失败检查**

`scripts/eval.ps1 -DryRun` 应校验语料/基线/API/DB/工具链，当前缺文件 → 非零退出。

- [ ] **Step 2: 运行确认失败**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun`
Expected: FAIL（缺语料/脚本）。

- [ ] **Step 3: 实现语料/runner**

固定环境/需求/设计/代码/执行/问题/视觉真值；baseline 7 指标；真实 LLM 流程读取/计算 sample；POST；阈值/基线报告；无 fake 模式；失败保留日志。

- [ ] **Step 4: 运行确认通过**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun`
Expected: PASS；真实 LLM 模式在 Task 22 验收中执行。

- [ ] **Step 5: 提交**

```bash
git add eval/v02 scripts/eval.ps1
git commit -m "feat(p6): add real-llm eval corpus and runner"
```

---

### Task 9: 全局搜索后端

**Files:**
- Create: `backend/internal/search/application/{service,ports}.go`
- Create: `backend/internal/search/infrastructure/providers/{version,design,delivery}.go`
- Create: `backend/internal/search/interfaces/httpapi/{routes,handler,dto}.go`
- Test: `backend/internal/search/application/service_test.go`、`.../handler_test.go`、`.../infrastructure/providers/*_test.go`

**Interfaces:**
- Consumes: Task 3b 的 `SearchVersions`/`SearchRequirements`/`SearchCases`/`SearchIssues`/`SearchReports`。
- Produces:
```go
type Provider interface{ Search(ctx context.Context, q string, limit int) ([]SearchItem, error) }
type Service struct{ providers map[string]Provider }
func (s *Service) Search(ctx, q string, types []string, limit int) (Snapshot, error)
```
- HTTP `GET /api/search`；5 类；degraded；url 深链。

- [ ] **Step 1: 写失败测试**

q 空/超长 400；types 未知 400；limit 1–20；5 组；单 Provider 失败 degraded；全失败 200 空 + degraded；url 格式；每类 1 查询（fake provider 计数）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/search/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

Provider 适配 Task 3b 端口（version+requirement / case / issue+report）；不跨表 SQL；不修改他域包；handler 权限登录。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/search/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/search backend/internal/{version,design,delivery}/application
git commit -m "feat(p6): add global search context"
```

---

### Task 10: CommandPalette 搜索前端

**Files:**
- Create: `frontend/src/features/search/{CommandPalette.tsx,useGlobalSearch.ts,api.ts,types.ts,CommandPalette.test.tsx}`
- Test: 同上
- 注意：不修改 `Topbar.tsx`；由 Task 22 Lead 统一接入。

**Interfaces:**
- Consumes: `GET /api/search`；`api-client`；路由深链。
- Produces: `CommandPalette` 组件 + Ctrl/Cmd+K 键盘处理；300ms 防抖/取消；键盘导航；最近搜索 localStorage 5 条；空/错/载/truncated/degraded。

- [ ] **Step 1: 写失败测试**

Ctrl+K 打开；输入防抖调用一次；↑↓/Enter/Esc；无结果显示空态；错误重试；最近搜索≤5；分组渲染。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/search/CommandPalette.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

`role=dialog`、focus trap、请求 abort；URL push；testid `global-search-*`；移动端全屏。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/search`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/search
git commit -m "feat(p6): add command palette"
```

---

### Task 11: 待办后端

**Files:**
- Create: `backend/internal/workspace/application/{service,ports}.go`
- Create: `backend/internal/workspace/infrastructure/providers/{version,design,execution,delivery}.go`
- Create: `backend/internal/workspace/interfaces/httpapi/{routes,handler,dto}.go`
- Test: `backend/internal/workspace/**/*_test.go`

**Interfaces:**
- Consumes: Task 3b 的 `CountRequirementClosure`/`SearchRequirements`（或等价只读方法）、`OpenHumanRequests`、`DeadWorkItems`、design/issue 计数/查询端口。
- Produces:
```go
type Provider interface{ Items(ctx, platform.Actor) ([]TodoItem, error) }
type Service struct{ providers []Provider }
func (s *Service) Snapshot(ctx, platform.Actor, limit int) (Snapshot, error)
```
- 6 kind；counts pre-limit；admin 组过滤；30s 轮询由前端。

- [ ] **Step 1: 写失败测试**

6 kind 数据源；counts 为 limit 前；非 admin 无 takeover/dead；排序；limit 1–200；深链；空态。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/workspace/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

Provider 聚合 Task 3b 端口；不修改他域包/不写表；admin-only 过滤在 handler/service；`limit` 截断 items 但 counts 全量。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/workspace/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/workspace
git commit -m "feat(p6): add todo aggregation context"
```

---

### Task 12: TodoDrawer 全局化前端

**Files:**
- Modify: `frontend/src/features/shell/TodoDrawer.tsx`
- Modify: `frontend/src/features/shell/TodoDrawer.test.tsx`
- Test: `TodoDrawer.test.tsx`
- 注意：Topbar 接入由 Task 22 Lead 统一完成；本任务不修改 Topbar。

**Interfaces:**
- Consumes: `GET /api/todos`；`api-client`；`useCurrentUser` admin。
- Produces: 全局 Drawer（无版本也可用）；分组/计数/深链/空错载；30s 轮询 + 路由刷新；不替换 P4 版本内数据源。

- [ ] **Step 1: 写失败测试**

无 versionId 仍显示入口/请求 `/api/todos`；6 组渲染；admin 组；普通用户无 takeover/dead；30s 轮询（fake timers）；错误重试；深链。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/shell/TodoDrawer.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

保留打开/关闭/焦点行为；分组折叠；counts 徽章；testid `todo-*`；不 resolve。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/shell`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/shell
git commit -m "feat(p6): globalise todo drawer"
```

---

### Task 13: Agent 中心只读后端（catalog/runs/quality）

**Files:**
- Create: `backend/internal/agent/application/agent_center.go`、`agent_center_test.go`
- Create: `backend/internal/agent/infrastructure/postgres/agent_center.go`
- Create: `backend/internal/agent/interfaces/httpapi/{agent_center_handler.go,agent_center_dto.go,agent_center_routes.go}`
- Test: 同上
- 注意：不修改 `routes.go`（共享 wiring 由 Task 22 Lead 收口）；`agent_center_routes.go` 只暴露 `RegisterAgentCenterRoutes`。

**Interfaces:**
- Consumes: `agent_profile`/`agent_run`/`work_item`；Task 3b 的 `VersionTitles`；quality latest eval 端口。
- Produces:
```go
func (s *AgentCenterService) Catalog(ctx) (Catalog, error)
func (s *AgentCenterService) Runs(ctx, RunFilter) ([]RunView, bool, error)
func (s *AgentCenterService) Quality(ctx) (QualityView, error)
func RegisterAgentCenterRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *AgentCenterHandler)
```
- HTTP：`GET /api/agent-center/{catalog,runs,quality}`（登录）；不返回 prompt/transcript/工具正文。

- [ ] **Step 1: 写失败测试**

14 profile/团队分组；stats/success_rate=done/(done+failed)；空统计 0；runs join work_item/version；非法 status 400；catalog 无 prompt；quality null。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

批量查询避免 N+1；version 标题经 application 端口；stats 聚合。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat(p6): add agent center read models"
```

---

### Task 14: 提示词版本存储/seed/回填

**Files:**
- Create: `backend/internal/agent/domain/prompt.go`、`prompt_test.go`
- Create: `backend/internal/agent/infrastructure/postgres/prompt_repo.go`、`prompt_repo_integration_test.go`
- Modify: `backend/internal/agent/application/profiles.go`（`DefaultPrompt`/`ProfileSeeds` 访问器）
- Create: `backend/internal/agent/application/prompt_seed.go`、测试
- Test: 同上

**Interfaces:**
- Consumes: Task 1 表；`AgentProfile`/seed。
- Produces:
```go
type PromptVariable struct{ Name, Description, Example, Source string }
type PromptRevision struct{ ID int64; ProfileKey string; Version int; SystemPrompt, Note, Source string; ActorID *int64; CreatedAt time.Time }
func ValidatePrompt(key, prompt string, catalog []PromptVariable) error
func (r *RuntimeRepository) AppendPromptRevision(ctx, profileKey, prompt, note, source string, actorID *int64) (int, error)
func (r *RuntimeRepository) ListPromptRevisions(ctx, profileKey string, limit int, beforeVersion *int) ([]PromptRevision, bool, error)
func EnsurePromptRevisions(ctx, repo) error // 无 revision 时插 v1(source=seed)
func DefaultPrompt(key) (string, bool)
```
- 写事务：锁 profile → max+1 → insert → update system_prompt/updated_at。

- [ ] **Step 1: 写失败测试**

空/超长/缺 marker/未知变量 400；seed 回填幂等；Append 版本递增/当前正文同步；no-op；并发/409；历史 DESC。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: FAIL。

- [ ] **Step 3: 实现 domain/repo/seed**

`[profile:<key>]` 强制；长度 32768；source enum；`DefaultPrompt` 从 `Profiles()` 同源；启动 seed 调 `EnsurePromptRevisions`（main.go 接线由 Lead 收口）。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: PASS；集成测试需 `EZTEST_TEST_DATABASE_URL`。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent/domain/prompt.go backend/internal/agent/infrastructure/postgres/prompt_repo.go backend/internal/agent/application/prompt_seed.go backend/internal/agent/application/profiles.go
git commit -m "feat(p6): add prompt revision storage and seed"
```

---

### Task 15: 提示词变量/渲染/预览 + 运行时接入

**Files:**
- Create: `backend/internal/agent/application/prompt_render.go`、`prompt_render_test.go`
- Create: `backend/internal/agent/application/prompt_catalog.go`（变量目录）
- Modify: `backend/internal/agent/application/context.go`（systemContent/minimalSystemContent 渲染）
- Modify: `backend/internal/agent/application/context_test.go`
- Test: 同上

**Interfaces:**
- Consumes: Task 14；`ContextInput`。
- Produces:
```go
type PromptVariable struct{ Name, Description, Example, Source string }
func PromptVariables(profileKey string) []PromptVariable
func RenderPrompt(profileKey, tmpl string, in ContextInput) (string, error)
```
- 变量：Now/ProfileKey/ProfileName/VersionID/VersionTitle/VersionStatus/TaskType/TaskGoal；executor + CaseRunID/WaveID/IntentID/CaseVersionID/CaseTitle/Priority。
- 渲染失败回退 `DefaultPrompt`；保留 `[profile:<key>]`；task/execution block 仍在正文后。

- [ ] **Step 1: 写失败测试**

变量替换；未知变量/语法错；长度；缺 marker；渲染失败回退默认；`[profile]` 保留；task/execution block 顺序；minimal 路径同样渲染。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/application`
Expected: FAIL。

- [ ] **Step 3: 实现渲染/目录/context 接入**

`text/template`+`missingkey=error`；无 FuncMap；变量值服务端生成；回退默认；不改变 system 分节结构。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/application`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent/application
git commit -m "feat(p6): render prompt templates with catalog variables"
```

---

### Task 16: Agent profile 配置/提示词 admin API + 审计

**Files:**
- Create: `backend/internal/agent/application/profile_admin.go`、测试
- Create: `backend/internal/agent/interfaces/httpapi/{profile_handler.go,profile_dto.go,profile_routes.go}`、测试
- Test: 同上
- 注意：不修改 `routes.go`（共享 wiring 由 Task 22 Lead 收口）；`profile_routes.go` 只暴露 `RegisterProfileRoutes`。依赖 Task 14/15（PromptRevision/ValidatePrompt/PromptVariables）。

**Interfaces:**
- Consumes: Task 14/15；P5 tool registry；settings/LLM 模型目录；audit。
- Produces:
```go
func (s *ProfileAdminService) List(ctx)([]ProfileView,error)
func (s *ProfileAdminService) Get(ctx,key)(ProfileDetail,error)
func (s *ProfileAdminService) Patch(ctx,actor,key,PatchInput)(ProfileView,bool,error)
func (s *ProfileAdminService) SavePrompt(ctx,actor,key,SavePromptInput)(ProfileView,int,bool,error)
func (s *ProfileAdminService) ResetPrompt(ctx,actor,key)(ProfileView,int,bool,error)
func (s *ProfileAdminService) RestorePrompt(ctx,actor,key,version)(ProfileView,int,bool,error)
func (s *ProfileAdminService) Revisions(ctx,key,limit,beforeVersion)([]PromptRevision,bool,error)
func (s *ProfileAdminService) Preview(ctx,key,tmpl string,sample map[string]string)(string,error)
func RegisterProfileRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *ProfileHandler)
```
- HTTP：`/api/agents/profiles*`、`/api/agents/tools`（admin）；`PATCH` 仅 model/budgets/allowed_tools；PUT prompt/reset/restore/revisions/preview；审计 `agent.profile.updated` 无正文。

- [ ] **Step 1: 写失败测试**

权限 403/401；PATCH 校验（tools registry/model/budgets）；PUT 版本/no-op/409/marker/变量；reset/restore；revisions 分页；preview；审计 metadata；catalog 不含 prompt。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: FAIL。

- [ ] **Step 3: 实现 service/handler**

字段缺省=不动；model null/空=默认；allowed_tools 完整覆盖；prompt 版本事务；错误 400/403/404/409；audit metadata 仅字段/版本/sha/长度。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat(p6): add agent profile prompt admin APIs"
```

---

### Task 17: Agent 中心前端（目录/运行/质量/配置/提示词编辑器）

**Files:**
- Create: `frontend/src/features/agent-center/{AgentCenterPage.tsx,ProfileEditor.tsx,PromptEditor.tsx,PromptVersionList.tsx,api.ts,types.ts,*.test.tsx}`
- Modify: `frontend/src/app/(app)/agents/page.tsx`
- Test: 同上

**Interfaces:**
- Consumes: `/api/agent-center/*`、`/api/agents/profiles*`、`/api/agents/tools`；`useIsAdmin`。
- Produces: `/agents` Tabs；普通用户只读无 prompt；admin 编辑 model/budgets/tools、提示词（变量 chip/预览/版本/diff/恢复/restore）；保存提示“下一次 run 生效”。

- [ ] **Step 1: 写失败测试**

14 profile/团队；只读态无 prompt/编辑；admin 保存调用 PATCH/PUT；变量插入；预览；版本列表/diff/恢复；400 field 展示；空/错/载。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/agent-center`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 spec §8.6；testid `agent-center-*`/`agent-prompt-*`；不显示 enabled/wrapup/triggers/MCP/Skill；不缓存 prompt 到公共 store。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/agent-center`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/agent-center "frontend/src/app/(app)/agents/page.tsx"
git commit -m "feat(p6): add agent center UI with prompt editor"
```

---

### Task 18: 全局问题/报告后端

**Files:**
- Create: `backend/internal/delivery/application/global.go`、测试
- Create: `backend/internal/delivery/infrastructure/postgres/global.go`、测试
- Create: `backend/internal/delivery/interfaces/httpapi/global_handler.go`、测试
- Modify: `backend/internal/delivery/interfaces/httpapi/routes.go`
- Test: 同上

**Interfaces:**
- Consumes: `issue`/`report`/`report_revision`；Task 3b 的 `VersionTitles`。
- Produces:
```go
func (s *IssueService) ListGlobal(ctx, GlobalIssueFilter) ([]GlobalIssueView, bool, error)
func (s *ReportService) ListGlobal(ctx, GlobalReportFilter) ([]GlobalReportView, bool, error)
```
- HTTP `GET /api/issues`、`GET /api/reports`（登录只读）；`before`+`limit`；version_title/url；merged/reopen；current revision。

- [ ] **Step 1: 写失败测试**

过滤/游标/limit；空；version_title；merged/reopen；report current revision/HTML；非法枚举 400；跨版本排序。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

本上下文 SQL；version 标题走 Task 3b 端口；不返回 content/内部路径；只读。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/delivery
git commit -m "feat(p6): add global issue and report read APIs"
```

---

### Task 19: 全局问题/报告前端

**Files:**
- Create: `frontend/src/features/delivery/{GlobalIssuesPage.tsx,GlobalReportsPage.tsx,global-api.ts,*.test.tsx}`
- Modify: `frontend/src/app/(app)/issues/page.tsx`、`reports/page.tsx`
- Test: 同上

**Interfaces:**
- Consumes: `GET /api/issues|/api/reports`；复用 `IssueDetailSheet`/报告详情/深链。
- Produces: `/issues`、`/reports` 跨版本只读列表/筛选/加载更多/空错载/深链；无写按钮。

- [ ] **Step 1: 写失败测试**

列表/筛选/分页；空错载；merged/reopen；report revision/HTML 打开失败；深链；无确认/合并按钮。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/delivery/Global`
Expected: FAIL。

- [ ] **Step 3: 实现**

复用现有表格/Sheet；URL 查询参数；`global-issues-page`/`global-reports-page` testid。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/delivery`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/delivery "frontend/src/app/(app)/issues/page.tsx" "frontend/src/app/(app)/reports/page.tsx"
git commit -m "feat(p6): add global issue and report pages"
```

---

### Task 20: 链路 v2 后端

**Files:**
- Modify: `backend/internal/chain/domain/view.go`
- Modify: `backend/internal/chain/application/service.go`、测试
- Modify: `backend/internal/chain/infrastructure/providers/*`（按需）
- Modify: `backend/internal/chain/interfaces/httpapi/{handler,dto}.go`、测试
- Test: 同上

**Interfaces:**
- Consumes: P4 chain 基座；settings；providers。
- Produces: query `q/abnormal_only/issue_status/issue_severity/requirement_limit`；`ChainCounts` 增量；typed meta；`degraded_layers`/`truncated_layers`/`replay.event_limit`；热加载/事件上限；错误语义。

- [ ] **Step 1: 写失败测试**

新过滤/诱导边/counts/meta；latest attempt；空版本 200 vs 不存在 404；非法枚举 400；degraded；热加载；replay_event_limit；查询预算 ≤10（SQL tracer）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

不重写 BuildChain；扩展过滤/counts/meta；settings 每请求读取；事件上限；provider 失败分层。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/chain
git commit -m "feat(p6): enhance exploration chain read model"
```

---

### Task 21: 链路 v2 前端

**Files:**
- Modify: `frontend/src/features/chain/{ChainTab.tsx,ChainFilters.tsx,ChainGraph.tsx,ChainDetailSheet.tsx,types.ts,*.test.tsx}`
- Test: 同上

**Interfaces:**
- Consumes: chain API 增量。
- Produces: q/仅异常/问题状态/严重级筛选；counts/coverage；typed meta；follow 自动居中；列表视图；分层错误/旧图保留/重连；按层截断横幅；1024 响应式；键盘/ARIA。

- [ ] **Step 1: 写失败测试**

筛选映射/URL；counts/meta 展示；follow setCenter；列表视图；截断/降级/错误保留；键盘。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm vitest run src/features/chain`
Expected: FAIL。

- [ ] **Step 3: 实现**

不重写画布；扩展 props/状态；`chain-*` testid；reduced-motion。

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend; pnpm typecheck; pnpm vitest run src/features/chain`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/chain
git commit -m "feat(p6): enhance exploration chain UI"
```

---

## Wave 2 — 接线、验收与收口（Lead 串行/半并行）

### Task 22: 共享 wiring + 全量后端/前端接线

**Files:**
- Modify: `backend/cmd/eztest/main.go`、`backend/internal/server/server.go`
- Modify: `frontend/src/features/shell/Topbar.tsx`
- Test: `server_p6_wiring_test.go`、Topbar 测试

**Interfaces:**
- Consumes: Task 3b 端口 + Task 5/9/11/13/16/18/20 服务与 `Register*Routes`。
- Produces: 所有 P6 handler 构造并挂载；`EnsurePromptRevisions` 启动调用；Topbar 搜索/待办接入；导航/Topbar 收口；真实依赖无 nil。

- [ ] **Step 1: 写失败测试**

`server_p6_wiring_test.go` 断言非 nil 时 20 路由 + 权限（admin/登录）；Topbar 测试断言搜索/待办可交互。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/server`；`cd frontend; pnpm vitest run src/features/shell/Topbar.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现接线**

按依赖顺序构造；错误 fatal；保持 P0–P5 路径不变；调用各 `Register*Routes`；`EnsurePromptRevisions` 启动 seed；Topbar 接入 CommandPalette/TodoDrawer。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend; ..\.tools\go\bin\go.exe build ./...; ..\.tools\go\bin\go.exe test -count=1 ./internal/server`；`cd frontend; pnpm typecheck; pnpm vitest run src/features/shell src/navigation`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/cmd/eztest backend/internal/server frontend/src/features/shell/Topbar.tsx
git commit -m "feat(p6): wire P6 contexts and shell"
```

---

### Task 23: P6 契约门禁 + 全量测试门禁

**Files:**
- Modify: `scripts/p6-contract-check.cjs`
- Modify: `backend/internal/db/migrate_test.go`（若未在 Task 1 完成）
- Modify: `scripts/p2–p5-contract-check.cjs`（若未在 Task 3 完成）
- Test: 全部契约脚本

**Interfaces:**
- Consumes: Task 2/22。
- Produces: `p6-contract-check.cjs` 校验 20 ops、33 migrations、3 表、3 settings、prompt 语义；P2–P5 前向兼容；全量 `scripts/test.ps1`。

- [ ] **Step 1: 写失败检查**

断言 `order=33`、OpenAPI 20 ops、db-schema 三表、settings 72、`agent-profiles-tools` §9；运行 FAIL。

- [ ] **Step 2: 运行确认失败**

Run: `node scripts/p6-contract-check.cjs`
Expected: FAIL。

- [ ] **Step 3: 实现检查**

按 spec 计数/needle；P5 gate 改为“增量存在/不硬编码阶段总数”。

- [ ] **Step 4: 运行确认通过**

Run: `node scripts/p2-contract-check.cjs; node scripts/p3-contract-check.cjs; node scripts/p4-contract-check.cjs; node scripts/p5-contract-check.cjs; node scripts/p6-contract-check.cjs`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add scripts/*contract-check.cjs backend/internal/db/migrate_test.go
git commit -m "test(p6): add contract gate and forward compatibility"
```

---

### Task 24: P6 验收脚本（真实 LLM + Chrome，S0–S9）

**Files:**
- Create: `scripts/p6-acceptance.ps1`
- Modify: `.tmp/p6-acceptance/`（运行时）
- Test: 脚本自身 + 真实环境运行

**Interfaces:**
- Consumes: Task 8/22/23；P5 验收数据/脚本；真实 DeepSeek/Chrome。
- Produces: S0–S9 断言、日志、退出码；不调用 fake LLM；DB 隔离 `eztest_v02_p6`。

- [ ] **Step 1: 写脚本骨架/失败断言**

实现 S0（33/72/契约/真实 LLM/executor）、S7（eval real_llm）、S1–S6/S8/S9；当前服务未接线 → FAIL。

- [ ] **Step 2: 运行确认失败**

Run: `powershell.exe -File scripts/p6-acceptance.ps1 -ApiPort 14345`
Expected: FAIL（环境/迁移/API）。

- [ ] **Step 3: 实现通过条件**

P5 数据克隆或完整真实 LLM 闭环；真实 LLM 重试/超时；每场景断言；证据写 `.tmp/p6-acceptance/`；失败保留日志。

- [ ] **Step 4: 运行确认通过**

Run: `powershell.exe -File scripts/p6-acceptance.ps1`
Expected: PASS=… FAIL=0；退出码 0；证据文件存在。

- [ ] **Step 5: 提交**

```bash
git add scripts/p6-acceptance.ps1
git commit -m "test(p6): add real-llm acceptance script"
```

---

### Task 25: P6 浏览器 E2E（真实 LLM）+ 截图

**Files:**
- Create: `scripts/p6-e2e.ps1`、`frontend/e2e/p6.spec.ts`
- Test: Playwright

**Interfaces:**
- Consumes: Task 22/24；真实 LLM API。
- Produces: admin/普通用户 E2E、截图、控制台错误检查；**不使用 fake-llm**。

- [ ] **Step 1: 写失败 spec**

质量/搜索/待办/Agent（编辑 prompt/版本/预览）/全局中心/链路 v2；权限；1280/1024；截图。

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend; pnpm exec playwright test e2e/p6.spec.ts`
Expected: FAIL（页面/API）。

- [ ] **Step 3: 实现脚本/E2E**

启动真实 LLM API/Web/executor；超时/重试；截图路径；无 fake 上游。

- [ ] **Step 4: 运行确认通过**

Run: `powershell.exe -File scripts/p6-e2e.ps1`
Expected: PASS；截图生成。

- [ ] **Step 5: 提交**

```bash
git add scripts/p6-e2e.ps1 frontend/e2e/p6.spec.ts
git commit -m "test(p6): add real-llm browser E2E"
```

---

### Task 26: 能力矩阵/ARTEX 矩阵/验收文档

**Files:**
- Create: `docs/version-V0.2/acceptance/p6/{acceptance.md,capability-parity.md,artex-prompt-parity.md}`
- Create: `docs/version-V0.2/acceptance/p6/screenshots/`（由 Task 25 产物复制）
- Test: 文档检查

**Interfaces:**
- Consumes: Task 24/25 证据；spec §8.9；capability-matrix。
- Produces: P0–P8 能力逐项入口/API/UI/证据/状态；ARTEX include/defer/na；验收命令/结果/截图。

- [ ] **Step 1: 写失败检查**

`acceptance.md` 缺 S0–S9/证据路径 → 文档检查失败；矩阵缺行。

- [ ] **Step 2: 运行确认失败**

Run: `powershell.exe -File scripts/check-docs.ps1`（若存在）或人工 `Select-String` 检查。
Expected: FAIL。

- [ ] **Step 3: 写文档**

矩阵覆盖 A1–I2，P9 标 P7；ARTEX 矩阵 38 项；每项指向 API/UI/测试证据；验收记录含真实 LLM/Chrome、截图、限制。

- [ ] **Step 4: 运行确认通过**

Run: 文档检查命令
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add docs/version-V0.2/acceptance/p6
git commit -m "docs(p6): add acceptance evidence and parity matrices"
```

---

### Task 27: 全量门禁、真实 LLM 验收、契约冻结、handoff、合并

**Files:**
- Modify: `docs/version-V0.2/contracts/*`（draft→frozen P6）
- Create: `docs/version-V0.2/phases/phase-06-handoff.md`
- Test: 全部门禁

**Interfaces:**
- Consumes: Task 23/24/25/26。
- Produces: 全绿证据、`frozen: P6`、handoff、merge `feat/v0.2-p6` → `feat/v0.2`。

- [ ] **Step 1: 跑全量门禁**

Run: `powershell.exe -File scripts/test.ps1`；`node scripts/p6-contract-check.cjs`；`powershell.exe -File scripts/p6-acceptance.ps1`；`powershell.exe -File scripts/p6-e2e.ps1`
Expected: 全 PASS；记录日志/截图。

- [ ] **Step 2: 冻结契约**

openapi `x-frozen: P6`；README 计数；db-schema/migrations/settings/agent-profiles-tools 冻结；不改 P0–P5 语义。

- [ ] **Step 3: 写 handoff**

按 contracts/handoff-template：完成范围、契约、证据、缺口（P6-D-12/21、ARTEX defer/na、P5 限制）、P7 输入、风险、停止声明。

- [ ] **Step 4: 复审 diff + 合并**

`git diff --stat feat/v0.2...`；确认无用户脏文件；`git switch feat/v0.2; git merge --no-ff feat/v0.2-p6`；记录 merge；不自动进入 P7。

- [ ] **Step 5: 提交/停止**

```bash
git add docs/version-V0.2/contracts docs/version-V0.2/phases/phase-06-handoff.md
git commit -m "docs(p6): freeze contracts and record handoff"
```

---

## Self-Review（写计划后执行）

- **Spec coverage:** Task 1–27 覆盖 spec §1.2 的 8 项 In；§5.2/5.3 指标、§6 搜索、§7 待办、§8 提示词、§9 全局中心、§10 链路、§12 验收、§14 契约均有任务。
- **Step scan:** 每任务有失败测试/失败验证/实现/通过验证/提交；测试含 spec 精确值（阈值、权限、6 kind、5 类、20 ops、33/72）。
- **Type consistency:** `EvalRun`/`Metric`/`ProfileView`/`PromptRevision`/`SearchItem`/`TodoItem`/`GlobalIssueView` 命名在 Task 4–21 一致；HTTP DTO 字段 snake_case 与 spec 一致。
- **Review Focus:** R1 真实 LLM 稳定性（Task 24/25）；R2 prompt 边界（Task 14/15/17）；R3 跨上下文空/性能（Task 6/9/11/18/20）；R4 权限脱敏（Task 16/13/24）；R5 迁移/门禁（Task 1/2/23/27）。
- **Proportion:** 计划按任务/接口/测试组织，未展开实现体；每步一个可执行动作。
- **缺口:** Task 8 真实 LLM 评测 flow 依赖 Task 24 数据，顺序已在 Task 24 S0→S7→S1 固定；Task 12/10/17/19/21 的 Topbar/nav/app 共享文件由 Task 22 Lead 收口。

---

*计划完成；实现阶段按用户要求无人值守、子代理并行、Lead 验收。*
