# EzTest 0.2 P2 用例设计（脑图/确认/导出）— 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (workstreams) or `executing-plans` to implement task-by-task. Steps use checkbox (`- [ ]`) syntax. Each task ends with a small commit and its own tests.
>
> **Spec:** `docs/superpowers/specs/2026-10-31-eztest-v02-p2-case-design-design.md`（唯一实现依据；冲突时以 spec 为准，spec 冲突再以设计文档为准）。
> **Phase:** `docs/version-V0.2/phases/phase-02-design.md`；**Prev handoff:** `phase-00-handoff.md`、`phase-01-handoff.md`。
> **Branch:** `feat/v0.2-p2`（已从 `feat/v0.2-p1` @75a899f 创建）。
> **执行方式:** 用户已批准“实现阶段无人值守”；Lead 统一验收。并行工作流 + 每任务独立 review。

**Goal:** 打通“新建设计对话 → 设计团队扫描待设计需求 → 实时脑图 → 用户对话/确认 → 导出”，并冻结 P2 契约、留下可复现测试/真实 LLM 证据与 handoff。

**Architecture:** 新增 `design` 限界上下文（场景/用例版本/确认/设计轮次/scope），扩展 `agent`（profiles/tasks/tools/runner/reconciler）、`version`（design 对话/指标/stale hook 调用点）、`storage`（`export_artifact` + 导出服务）与 `frontend`（`features/design`）。图写路径单一 `case.graph_propose`，自然键去重 + advisory lock；确认/导出走用户 API；真实图/链路由 SSE 驱动前端刷新。

**Tech Stack:** Go 1.24+ / PostgreSQL 15+ / Next.js 16 + React 19 + @xyflow/react / Vitest / Playwright / PowerShell 验收脚本 / DeepSeek `deepseek-flash`（真实 LLM 验收）。

## Global Constraints

- 设计文档、决策日志、参数默认是权威；**不修改 `docs/version-V0.2/**` 设计文档**；只写 contracts、迁移、测试证据、handoff、spec/plan。
- 本阶段只做 P2；P3+ 只进 handoff/backlog，不建表、不提前改契约。
- `.env` 严格 4 项 `EZTEST_*`；P2 **不新增 env/设置键**；`auto_design_new_requirements` 是既有 `version` 列。
- 主键雪花 `BIGINT`；DB/Go/JSON/事件中的实体 ID 为十进制字符串；URL 参数为字符串。
- 命名方案 A（Agent.md §6.9/D34）：`case_scenario`、`export_artifact`、`sort_order`、`line_no`、`file_format`、`size_bytes`、`content_object_id`；PK/UK/IDX 显式 `pk_/uk_/idx_`；跨上下文只逻辑引用。
- 不恢复 0.1：无设计评审页签/整版冻结；`case.confirm` **不注册为模型工具**；不建 baseline/review 表。
- 测试双轨：deterministic fake LLM 做单元/集成/故障注入/UI 回归；**真实 LLM 必跑** API 验收 + 浏览器主链路 + 早期产图探针；真实失败/缺 key → handoff `blocked`，不以 fake 替代。
- 单实例假设沿用 P1；不承诺多副本实时。
- 每任务小步提交；只 `git add` 本任务文件；不纳入工作树既有 dirty 文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1` 等）。
- 迁移只追加；不改已应用迁移；`order.txt` 唯一顺序。
- 阶段退出：`scripts/test.ps1` 全绿 + P2 API 验收 + fake E2E + 真实 LLM API/浏览器 + 契约冻结 + handoff。

## Review Focus（五类最易翻车点，均绑定到任务测试）

1. **契约漂移**：新增 paths/schemas/events/表/任务/工具必须与 spec 一致；`T1` 的 OpenAPI/事件检查 + `T8` handler 测试 + `T21` 全量 gate 覆盖。
2. **多 SUT 身份混用**：`version_sut_id`/`sut_id`/page/api/database 目标必须按版本绑定校验；`T3` 目标校验测试 + `T5` propose 跨 SUT 用例测试覆盖。
3. **图重复/丢更新**：自然键去重、内容 hash 幂等、`base_graph_hash` 409；`T5` 重复 propose/base hash 测试覆盖。
4. **确认/stale 原子性与重试**：三粒度确认整批事务、幂等、并发；stale 传播/重设计/superseded；`T6` + `T7` + `T19` 验收覆盖。
5. **旧假设回流**：无 review/baseline/整版冻结、无 `case.confirm` 工具、无“结束本轮”；`T11` 工具清单测试 + `T15/T16` UI 测试 + `T21` 契约检查覆盖。

## 并行执行图

```text
Wave 0（Lead，串行）
  T1 契约增量 ──> T2 迁移/order/迁移测试

Wave 1（并行；共享契约已冻结）
  backend-design: T3 domain -> T4 repo -> T5 graph -> T6 confirm/stale -> T7 planner -> T8 HTTP
                  T9 storage export_artifact（可与 T3-T8 并行） -> T10 export renderers/service
  backend-agent:  T11 profiles/tools/fake LLM（依赖 T5/T6/T7 接口签名，可用接口 stub 先写）
                  T12 runner 扩展 -> T13 version 集成（conversation kind/reconciler/stale Update/metrics）
  frontend:       T14 API/types/SSE -> T15 mindmap/detail/DesignTab 壳
                                     -> T16 chat/export/switch/CTA 组件
                                     -> T17 VersionDetailPage/CTA/深链集成

Wave 2（Lead，串行/收敛）
  T18 server/main 装配 + server 集成测试
  T19 p2-acceptance.ps1（fake）+ p2-e2e.ps1（fake）
  T20 真实 LLM 探针/API 验收/浏览器主链路
  T21 契约冻结 + 全量 gate + handoff
  T22 独立 whole-branch review + 修复
```

**写作用域（advisory）**：Lead = `contracts/**`、`backend/internal/db/migrations/**`、`backend/cmd/eztest/**`、`backend/internal/server/**`、`scripts/p2-*`、handoff；backend-design = `backend/internal/design/**` + `backend/internal/storage/{domain,application,infrastructure/postgres}/*export*`；backend-agent = `backend/internal/agent/**` + `backend/internal/version/**`；frontend = `frontend/src/features/design/**`、`VersionDetailPage.tsx`、`DesignCTA.tsx`、`frontend/src/lib/api-client.ts`。共享文件只由 Lead 写。

**运行环境（Lead T0）**
- `go`: `.tools/go/bin/go.exe`；`node`/`pnpm` 已可用；PostgreSQL `localhost:5433`。
- 本地 `.env` 增加（不提交）`EZTEST_TEST_DATABASE_URL=<EZTEST_DATABASE_URL 的 eztest_v02 替换为 eztest_v02_test>`；验证 `scripts/test.ps1` 基线。
- 验收端口约定沿用 P1：API 14333、fake LLM 13334（E2E 13335）、Web 16868、隔离库 `eztest_v02_e2e`、登录 `admin/admin123`。

---

## Task 1: P2 契约增量与命名冻结（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/openapi.yaml`（paths/schemas/枚举/版本详情增量；T1 临时 `x-draft: P2`，T20 翻为 `x-frozen: P2`）
- Modify: `docs/version-V0.2/contracts/events.md`（§11 P2 事件）
- Modify: `docs/version-V0.2/contracts/db-schema.md`（§8 P2 10 表）
- Modify: `docs/version-V0.2/contracts/migrations-order.md`（§6 追加）
- Modify: `docs/version-V0.2/contracts/task-types.md`（§3 P2 任务）
- Modify: `docs/version-V0.2/contracts/agent-profiles-tools.md`（§5 P2 profiles/tools）
- Modify: `docs/version-V0.2/contracts/settings.md`（§4b：不新增键）
- Modify: `docs/version-V0.2/contracts/README.md`（P2 增量状态；T20 冻结）
- Create: `scripts/p2-contract-check.cjs`（解析 YAML、检查 paths/schemas/draft-frozen/事件/表名）
- Test: `scripts/p2-contract-check.cjs`

**Interfaces (produces):** 所有 P2 HTTP path/schema 名称、事件名、表名/迁移名、任务类型、profile/tool 名称以 spec §3/§5/§6/§7/§11 为准。

- [ ] **Step 1: 写契约检查脚本（先失败）**
  断言 P2 paths 存在：`/api/versions/{id}/design/graph`、`/design/overview`、`/design/cases/{caseId}`、`/design/confirmations`、`/api/versions/{id}/exports`、`/api/exports/{exportId}`、`/api/exports/{exportId}/download`；schemas：`DesignGraphView`、`CaseDetailView`、`CaseConfirmationRequest/Result`、`ExportRequest/View`；版本详情含 `pending_design_count`/`case_counts`；`ConversationKind` 创建允许 `design`；根标记为 `x-draft: P2`（T20 才翻 `x-frozen`）。
- [ ] **Step 2: 运行并确认失败**
  `node scripts/p2-contract-check.cjs` → 非 0，报缺失 path/schema。
- [ ] **Step 3: 写入契约**
  按 spec §7 写 OpenAPI（ID 字符串、错误/分页 envelope 沿用 P0/P1）；events.md 写 10 个 P2 事件与 scope；db-schema.md 写 10 表（命名方案 A）；migrations-order 追加 2 文件；task-types/agent-profiles-tools/settings/README 增量。
- [ ] **Step 4: 运行通过**
  `node scripts/p2-contract-check.cjs` → 0；`node -e "..."`（YAML 解析）→ OK。
- [ ] **Step 5: Commit**
  `git add docs/version-V0.2/contracts scripts/p2-contract-check.cjs && git commit -m "feat(p2): add P2 contracts and naming freeze"`

---

## Task 2: P2 迁移、order 与迁移集成测试（Lead）

**Files:**
- Create: `backend/internal/db/migrations/design_0001_case_model.sql`
- Create: `backend/internal/db/migrations/storage_0002_export_artifact.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/db/migrate_integration_test.go`
- Test: `backend/internal/db/migrate_integration_test.go`

**Interfaces (produces):** 10 表（`case_scenario`、`case_entity`、`case_version`、`case_step`、`case_assertion`、`case_source`、`case_confirmation`、`design_round`、`design_scope`、`export_artifact`）；PK/UK/IDX 命名按 spec §3。

- [ ] **Step 1: 更新迁移测试（先失败）**
  `applied` 期望 15→17；`expected` 追加 10 表；新增 `TestP2SchemaNames`：查询 `pg_constraint`/`pg_indexes`，断言 `pk_case_*`、`uk_case_*`、`idx_case_*`、`pk_export_artifact`、`uk_export_artifact_*` 存在，且无 `scenario`/`export` 旧表名。
- [ ] **Step 2: 运行失败**
  `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/db -run TestMigrateAppliesAllTablesAndIsIdempotent -v` → 失败（15≠17 / 表缺失）。
- [ ] **Step 3: 写 SQL 与 order**
  按 spec §3 的列/约束/索引/CHECK 写两个迁移；只追加 DDL；同上下文 FK；跨上下文逻辑引用。
- [ ] **Step 4: 通过**
  同一命令 → PASS；再跑一次确认幂等。
- [ ] **Step 5: Commit**
  `git add backend/internal/db && git commit -m "feat(p2): add design and export migrations"`

---

## Task 3: design domain 模型、校验、内容 hash（backend-design）

**Files:**
- Create: `backend/internal/design/domain/{doc.go,scenario.go,case.go,confirmation.go,round.go,errors.go,hash.go}`
- Create: `backend/internal/design/domain/*_test.go`
- Test: `backend/internal/design/domain/...`

**Interfaces (produces):**
```go
const (
  CaseStatusDraft = "draft"; CaseStatusConfirmed = "confirmed"
  CaseStatusStale = "stale"; CaseStatusSuperseded = "superseded"
)
type Proposal struct { RequirementID, RequirementVersionID int64; BaseGraphHash string; Scenarios []ScenarioProposal; Cases []CaseProposal }
type ProposalContext struct { VersionID, RequirementID, RequirementVersionID int64; CurrentHash string; AllowedVersionSUTs []int64; PageToSUT map[int64]int64; APIToSUT map[int64]int64; DBTOSUT map[int64]int64; Limits Limits }
func ValidateProposal(in Proposal, ctx ProposalContext) error
func ComputeCaseContentHash(v CaseVersion) (string, error)
func ValidateConfirmationScope(scope string) error
```
字段/长度/枚举/步骤 seq 连续/断言 step_seq/目标归属/数量上限按 spec §3/§4/§5.3。

- [ ] **Step 1: 写失败测试**
  `TestValidateProposalRejectsUnknownVersionSUT`、`...RejectsNonContiguousSteps`、`...RejectsBadAssertionStep`、`...AllowsCrossSUTSteps`、`...NaturalKey`、`TestComputeCaseContentHashStable`（同内容同 hash、顺序无关的 JSON 规范化）、`TestProposalLimits`（>3 场景/>10 用例）。
- [ ] **Step 2: 运行失败**
  `cd backend; ..\.tools\go\bin\go.exe test ./internal/design/domain -v` → 失败（未定义）。
- [ ] **Step 3: 实现 domain**
  纯函数/值对象；不导入 pgx/http/LLM；不写 DB。
- [ ] **Step 4: 通过**
  同一命令 → PASS；`go test ./internal/design/domain`。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/domain && git commit -m "feat(p2): add design domain model and validation"`

---

## Task 4: design PostgreSQL 仓储与集成测试（backend-design）

**Files:**
- Create: `backend/internal/design/domain/repository.go`
- Create: `backend/internal/design/infrastructure/postgres/{repo.go,graph.go,confirm.go,planner.go}`
- Create: `backend/internal/design/infrastructure/postgres/integration_test.go`
- Test: 同上

**Interfaces (consumes T3; produces):**
```go
type Repository interface {
  ReadGraph(ctx context.Context, versionID int64) (GraphData, error)
  ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (CaseData, error)
  WithRequirementLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, Tx) error) error
  Confirm(ctx context.Context, versionID int64, sel ConfirmSelection, actorID int64, batchID string) (ConfirmResult, error)
  // planner/queries: scopes, rounds, candidate versions, stale propagation, metrics
}
```
`Tx` 提供场景/用例/版本/步骤/断言的读写；事务内 `pg_advisory_xact_lock(versionID, requirementID)`；所有 ID 雪花。

- [ ] **Step 1: 写失败集成测试**
  `TestDesignRepoTxRollsBack`、`TestProposeNaturalKeyDedup`、`TestConfirmAtomicAndIdempotent`、`TestStalePropagation`、`TestPlannerQueries`（pending/scope/rounds）、`TestCaseCounts`。使用 `testutil.NewDB(t)`。
- [ ] **Step 2: 运行失败**
  `powershell.exe -File scripts/test-integration.ps1 ./internal/design/infrastructure/postgres -v` → 失败。
- [ ] **Step 3: 实现仓储**
  pgx；`ReadGraph` 聚合查询（避免 N+1）；`WithRequirementLock` 事务；`Confirm` 行锁；错误映射 `platform.NotFound/StateConflict`；不改 P0/P1 表。
- [ ] **Step 4: 通过**
  同一命令 → PASS；再跑一次幂等。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/infrastructure backend/internal/design/domain/repository.go && git commit -m "feat(p2): add design postgres repositories"`

---

## Task 5: 图读取与 `case.graph_propose` 服务（backend-design）

**Files:**
- Create: `backend/internal/design/application/{ports.go,graph.go,views.go}`
- Create: `backend/internal/design/application/graph_test.go`
- Test: 同上

**Interfaces (consumes T3/T4; produces):**
```go
type GraphService struct{ /* repo, version reader, resource catalog, events, audit, ids, clock */ }
func (s *GraphService) ReadGraph(ctx context.Context, versionID int64) (GraphView, error)
func (s *GraphService) GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (CaseDetail, error)
func (s *GraphService) Overview(ctx context.Context, versionID int64) (Overview, error)
func (s *GraphService) Propose(ctx context.Context, versionID int64, in ProposeInput, actorID, runID int64) (ProposeResult, error)
```
Propose：作用域校验 → advisory lock → `base_graph_hash` 比对 → 自然键/ID 解析 → domain 校验 → 事务写 → 事件/审计。返回 `{scenario_ids,case_ids,case_version_ids,graph_hash}`。

- [ ] **Step 1: 写失败测试**
  `TestProposeCreatesScenarioCaseAndSteps`、`TestProposeRejectsBaseHashMismatch`、`TestProposeIsIdempotentForSameContent`、`TestProposeUpdatesNaturalKeyInsteadOfDuplicating`、`TestProposeUpdatesConfirmedCaseAsNewDraft`（旧 confirmed 仍 confirmed、current 指向新 draft）、`TestReadGraphIncludesStaleAndCounts`、`TestCrossSUTProposalAllowed`。
- [ ] **Step 2: 运行失败**
  `cd backend; ..\.tools\go\bin\go.exe test ./internal/design/application -run 'TestPropose|TestReadGraph' -v`。
- [ ] **Step 3: 实现 service**
  纯 application；端口注入；事件 `design.graph_delta`/`design.case_version_created`；审计 `case.created/updated`。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/application && git commit -m "feat(p2): add design graph read and propose service"`

---

## Task 6: 确认、stale/superseded、Critic/Data Planner 提交服务（backend-design）

**Files:**
- Create: `backend/internal/design/application/{confirmation.go,stale.go,review.go,data_plan.go}`
- Create: `backend/internal/design/application/{confirmation_test.go,review_test.go,data_plan_test.go}`
- Test: 同上

**Interfaces (consumes T3/T4; produces):**
```go
func (s *GraphService) Confirm(ctx context.Context, versionID int64, in ConfirmInput, actorID int64) (ConfirmResult, error)
func (s *StaleService) MarkRequirementChanged(ctx context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error)
func (s *GraphService) SubmitReview(ctx context.Context, versionID, requirementID, runID int64, in ReviewInput) (ReviewResult, error)
func (s *GraphService) SubmitDataPlan(ctx context.Context, versionID, caseVersionID, runID int64, in DataPlanInput) (DataPlanResult, error)
```
确认：`scope=case|scenario|requirement`；整批事务；已确认 skipped；无 draft 409；stale 跳过；batch_id 生成；旧 confirmed/stale→superseded；事件 `design.case_confirmed`/`design.case_version_superseded`/`design.case_version_stale`。
Review：落 `team_room_post(kind=finding)` + `design.review_submitted`；不建人工评审表。DataPlan：仅当前 draft；复制 steps/assertions/sources 生成新 draft revision。

- [ ] **Step 1: 写失败测试**
  `TestConfirmCaseIdempotent`、`TestConfirmScenarioConfirmsOnlyDrafts`、`TestConfirmRequirementRejectsWhenNoDraft`、`TestConfirmRequirementRequiresClarifiedAndSameReqVersion`、`TestConfirmSupersedesOlderVersions`、`TestMarkRequirementChangedMarksOnlyConfirmedCurrent`、`TestStaleHookIdempotent`、`TestSubmitReviewPostsFinding`、`TestSubmitDataPlanCreatesDraftRevision`、`TestSubmitDataPlanRejectsConfirmed`。
- [ ] **Step 2: 运行失败**
  `go test ./internal/design/application -run 'TestConfirm|TestMarkRequirement|TestSubmit' -v`。
- [ ] **Step 3: 实现**
  共享事务/锁/快照；事件/审计；`MarkRequirementChanged` 返回十进制字符串 ID；DataPlan 版本不可变。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/application && git commit -m "feat(p2): add confirmation, stale and review/data-plan services"`

---

## Task 7: Planner、scope/round、pending、gap、reconciler（backend-design）

**Files:**
- Create: `backend/internal/design/application/{planner.go,planner_ports.go,reconciler.go}`
- Create: `backend/internal/design/application/planner_test.go`
- Test: 同上

**Interfaces (consumes T4/T6; produces):**
```go
type Planner struct{ /* repo, enqueuer, team room, events, clock, ids */ }
func (p *Planner) StartRound(ctx context.Context, versionID, conversationID, actorID int64) (RoundView, error)
func (p *Planner) ScanVersion(ctx context.Context, versionID int64) error
func (p *Planner) Reconcile(ctx context.Context) error
func (p *Planner) RequestGap(ctx context.Context, in GapInput) error
func (p *Planner) CountPendingDesign(ctx context.Context, versionID int64) (int, error)
func (p *Planner) CaseCounts(ctx context.Context, versionID int64) (CaseCounts, error)
type TaskEnqueuer interface { Ensure(ctx context.Context, in TaskInput) (bool, error) }
type ConversationCreator interface { CreateDesign(ctx context.Context, versionID int64, title string) (int64, error) }
```
D25：最新 design 对话永远活跃；无设计对话 + auto 开关 → 自动建对话+round；已有 → 自动并入最新 round；不关闭。

- [ ] **Step 1: 写失败测试**
  `TestStartRoundActivatesAllPendingAndCreatesTasks`、`TestStartRoundIdempotent`、`TestAutoJoinIntoLatestRound`、`TestAutoDesignOnlyWhenNoConversation`、`TestPendingCountExcludesActiveScope`、`TestScanCreatesCriticAndDataPlanOnlyForDrafts`、`TestRequestGapEnsuresScopeAndTask`、`TestReconcileIdempotent`。
- [ ] **Step 2: 运行失败**
  `go test ./internal/design/application -run 'TestStartRound|TestAuto|TestPending|TestScan|TestRequestGap|TestReconcile' -v`。
- [ ] **Step 3: 实现**
  幂等 ensure 键按 spec §5.1；`design.round_started`/`design.scope_activated` 事件；团队帖子经 TeamRoom port。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/application && git commit -m "feat(p2): add design planner, scope and reconciler"`

---

## Task 8: design HTTP API（backend-design）

**Files:**
- Create: `backend/internal/design/interfaces/httpapi/{handler.go,dto.go,graph_handler.go,confirmation_handler.go,export_handler.go}`
- Create: `backend/internal/design/interfaces/httpapi/*_test.go`
- Test: 同上

**Interfaces (consumes T5/T6/T7; produces):** 路由集合：
`GET /api/versions/{id}/design/graph`、`GET /design/overview`、`GET /design/cases/{caseId}`、`POST /design/confirmations`、`GET /api/versions/{id}/exports`、`POST /api/versions/{id}/exports`、`GET /api/exports/{exportId}`、`GET /api/exports/{exportId}/download`。

- [ ] **Step 1: 写失败 handler 测试**
  使用 `httptest` + fake services：`TestGraphHandlerReturnsEnvelope`、`TestConfirmHandlerValidation`（scope/target/400/409）、`TestExportHandlerRejectsUnsupportedFormat`、`TestDownloadSetsContentDispositionAndVerifiesHash`、`TestClosed?`（无关闭路由，断言 404）。
- [ ] **Step 2: 运行失败**
  `go test ./internal/design/interfaces/httpapi -v`。
- [ ] **Step 3: 实现**
  Bearer 由 server 中间件负责；ID 解析为 int64，响应 ID 用 `platform.Format`；错误 envelope 沿用 P0；分页 `{data,next_cursor,has_more}`。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/design/interfaces && git commit -m "feat(p2): add design HTTP API"`

---

## Task 9: storage `export_artifact` 域/仓储/服务（backend-design）

**Files:**
- Create: `backend/internal/storage/domain/export.go`
- Create: `backend/internal/storage/application/{export.go,export_ports.go}`
- Create: `backend/internal/storage/infrastructure/postgres/export.go`
- Create: `backend/internal/storage/application/export_test.go`、`backend/internal/storage/infrastructure/postgres/export_integration_test.go`
- Test: 同上

**Interfaces (produces):**
```go
type ExportArtifact struct { ID, VersionID, OwnerID int64; OwnerKind string; Revision int; Format string; ContentObjectID string; Size int64; MIME, Filename, Status string; CreatedBy *int64; CreatedAt time.Time }
func (s *ExportService) Create(ctx context.Context, in CreateExportInput, actorID int64) (ExportArtifact, error)
func (s *ExportService) Get(ctx context.Context, id int64) (ExportArtifact, error)
func (s *ExportService) List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]ExportArtifact, bool, error)
func (s *ExportService) Open(ctx context.Context, id int64) (ExportArtifact, io.ReadCloser, error)
```
Create：写内容账本（`Upload`/`AddRef`，owner_kind=`export`）→ 插入 export_artifact（revision=max+1）→ sha/size 校验。P2-D-29 例外。

- [ ] **Step 1: 写失败测试**
  `TestExportServiceWritesContentAndRef`、`TestExportRevisionIncrements`、`TestExportFilenameAndMIME`、`TestExportOpenVerifiesHash`、`TestExportListCursor`。
- [ ] **Step 2: 运行失败**
  `go test ./internal/storage/... -run TestExport`；集成 `scripts/test-integration.ps1 ./internal/storage/infrastructure/postgres -v`。
- [ ] **Step 3: 实现**
  不改 P0 content service 语义；新增 export 服务；DB 列用规范名。
- [ ] **Step 4: 通过**
  单元+集成 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/storage && git commit -m "feat(p2): add export artifact storage service"`

---

## Task 10: 导出渲染器与导出应用服务（backend-design）

**Files:**
- Create: `backend/internal/design/domain/export_{markdown,mermaid,opml,csv}.go`
- Create: `backend/internal/design/domain/export_test.go`
- Create: `backend/internal/design/application/{export.go,export_ports.go}`
- Create: `backend/internal/design/application/export_test.go`
- Create: `backend/internal/design/infrastructure/storageexport/adapter.go`
- Test: 同上

**Interfaces (consumes T5/T9; produces):**
```go
type ExportStore interface { Create(ctx, in ExportRecord) (ExportRecord, error); Get(...); List(...); Open(...) }
func (s *ExportService) Create(ctx context.Context, versionID int64, in ExportInput, actorID int64) (ExportView, error)
func (s *ExportService) List/Get/Open(...)
```
格式：Markdown / Mermaid / OPML / CSV（一行一步骤，RFC4180）；`export_artifact` 元数据；`design.export_ready` 事件。

- [ ] **Step 1: 写失败 golden 测试**
  `TestMarkdownContainsHierarchyAndSteps`、`TestMermaidEscapesLabelsAndTruncates`、`TestOPMLParsesAndHasNotes`、`TestCSVOneRowPerStepAndEscapes`、`TestExportSkipsSupersededAndIncludesStale`、`TestExportSameContentHashStable`。
- [ ] **Step 2: 运行失败**
  `go test ./internal/design/domain ./internal/design/application -run Test*Export -v`。
- [ ] **Step 3: 实现**
  纯函数渲染器；应用服务读图→渲染→`ExportStore.Create`→事件/审计；文件名 `case-export-{id}.{ext}`。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/design && git commit -m "feat(p2): add case export renderers and service"`

---

## Task 11: Agent profiles、tasks、tools、runner 扩展、fake LLM（backend-agent）

**Files:**
- Modify: `backend/internal/agent/application/profiles.go`（5 profiles + prompts）
- Modify: `backend/internal/agent/application/tools/registry.go`（P2 tool names/Deps/Scope）
- Create: `backend/internal/agent/application/tools/design.go`
- Modify: `backend/internal/agent/application/{ports_agent.go,runner.go}`（profile 映射/payload/context/outputs/team room）
- Modify: `backend/internal/agent/infrastructure/versionadapter/conversations.go`（kind）或新增 `designadapter`
- Create: `backend/internal/agent/infrastructure/designadapter/adapter.go`
- Modify: `scripts/fake-llm.cjs`
- Create: `scripts/fake-llm-p2.test.cjs`
- Test: `backend/internal/agent/application/tools/design_test.go`、`runner_test.go`、`internal/agent/infrastructure/designadapter/adapter_test.go`、`scripts/fake-llm-p2.test.cjs`

**Interfaces (produces):**
```go
const (
  WorkTypeDesignRound = "design.round"; WorkTypeDesignRequirement = "design.requirement"
  WorkTypeDesignGap = "design.gap"; WorkTypeDesignCritic = "design.critic"; WorkTypeDesignDataPlan = "design.data_plan"
)
func ProfileKeyForWorkType(workType string) (string, bool) // design.* -> profiles
func ProfileKeyForConversationKind(kind string) (string, bool) // design -> design_host
type WorkItemPayload struct { ConversationID, MessageID, AttachmentID, RequirementID, RequirementVersionID, CaseID, CaseVersionID, RoundID int64; Trigger string }
```
Tools：`design.graph_read`、`case.graph_propose`、`design.review_submit`、`design.data_plan_submit`；`case.confirm` 不注册。
`tools.DesignPort`：
```go
ReadGraph(ctx, versionID, requirementID, caseID int64) (any, error)
ProposeGraph(ctx, versionID int64, in tools.GraphProposal, actorID, runID int64) (any, error)
SubmitReview(ctx, versionID, requirementID, runID int64, in tools.ReviewInput) (any, error)
SubmitDataPlan(ctx, versionID, caseVersionID, runID int64, in tools.DataPlanInput) (any, error)
```
Runner：design Host 写 assistant message；planner 写 assistant message + team post；author/critic/data planner 写 team post；team room 使用 `design` 频道。

- [ ] **Step 1: 写失败测试**
  `TestP2ProfileSeeds`（5 key/tools/budgets/prompt marker）、`TestProfileKeyForWorkTypeP2`、`TestGraphProposeToolValidatesScopeAndPayload`、`TestCaseConfirmIsNotRegistered`、`TestRunnerDesignTaskContextAndOutputs`、`TestFakeLLMDesignProfiles`（host/planner/author/critic/data planner 的工具调用序列）。
- [ ] **Step 2: 运行失败**
  `go test ./internal/agent/... -run 'TestP2|TestGraphPropose|TestCaseConfirm|TestRunnerDesign|TestProfileKeyForWorkTypeP2' -v`；`node --test scripts/fake-llm-p2.test.cjs`。
- [ ] **Step 3: 实现**
  工具 schema 不含 versionId；scope 注入扩展；自然键/hash 由 design 服务负责；fake 保持 P0/P1 行为，新增 design 分支（含 design_host 修改用例、case_author 产图、critic/data planner 调用）。
- [ ] **Step 4: 通过**
  同一命令 → PASS。
- [ ] **Step 5: Commit**
  `git add backend/internal/agent scripts/fake-llm.cjs scripts/fake-llm-p2.test.cjs && git commit -m "feat(p2): add design agent profiles, tools and runner support"`

---

## Task 12: version 集成（design 对话、reconciler kind、Update stale、metrics）（backend-agent）

**Files:**
- Modify: `backend/internal/version/application/{conversation.go,requirement.go,service.go,views.go}`
- Modify: `backend/internal/version/application/{conversation_ports.go,requirement_ports.go}`（DesignStarter/DesignMetrics/CaseStaleHook 调用）
- Modify: `backend/internal/version/infrastructure/conversation.go`（ListReconcilable 含 kind）
- Modify: `backend/internal/version/interfaces/httpapi/{conversation_handler.go,dto.go,handler.go}`
- Create/modify tests: `backend/internal/version/application/*_test.go`、`backend/internal/version/interfaces/httpapi/*_test.go`
- Test: 同上

**Interfaces (produces):**
```go
type DesignStarter interface { StartRound(ctx context.Context, versionID, conversationID, actorID int64) error }
type DesignMetrics interface { CountPendingDesign(ctx context.Context, versionID int64) (int, error); CaseCounts(ctx context.Context, versionID int64) (CaseCounts, error) }
type CaseCounts struct { Draft, Confirmed, Stale int }
```
- `ConversationService.Create` 允许 `kind=design`，标题默认 `用例设计 YYYY-MM-DD HH:mm`，opening `chat.turn` team=design；澄清不变。
- 对话 HTTP handler 在 design 创建成功后 best-effort 调 `DesignStarter.StartRound`。
- `ReconcilerConversation.Kind`；P1 对话 reconciler 对 design 跳过 `requirement.curate`；opening/reply chat.turn 保留。
- `RequirementService.Update` 在 AddRevision 后调用 stale hook；`requirement.version_created` payload 加 `affected_case_version_ids`。
- `VersionView` 增加 `CaseCounts`；`PendingDesignCount` 通过 `DesignMetrics`（失败回退 clarified 计数）。

- [ ] **Step 1: 写失败测试**
  `TestCreateDesignConversationAllowsDesignAndEnqueuesDesignTeam`、`TestConversationHandlerCallsStartRound`、`TestListReconcilableIncludesKindAndSkipsCurateForDesign`、`TestUpdateCallsStaleHookAndEventCarriesAffected`、`TestVersionViewUsesDesignMetrics`、`TestVersionViewFallsBackToClarifiedOnMetricsError`、`TestVersionDTOCaseCounts`。
- [ ] **Step 2: 运行失败**
  `go test ./internal/version/... -run 'TestCreateDesign|TestConversationHandlerCalls|TestListReconcilable|TestUpdateCalls|TestVersionView' -v`。
- [ ] **Step 3: 实现**
  端口在 version application 定义；design 实现由 main 注入；不导入 design infrastructure；保持 P1 行为。
- [ ] **Step 4: 通过**
  同一命令 → PASS；P1 version tests 不回归。
- [ ] **Step 5: Commit**
  `git add backend/internal/version && git commit -m "feat(p2): integrate design conversations, metrics and stale hook"`

---

## Task 13: 前端 API/types/SSE/下载 helper（frontend）

**Files:**
- Create: `frontend/src/features/design/{api.ts,types.ts}`
- Create tests: `frontend/src/features/design/api.test.ts`
- Modify: `frontend/src/features/clarification/{types.ts,useVersionEvents.ts}`（design 事件/kind）
- Modify: `frontend/src/features/versions/{types.ts,api.ts}`（pending/case_counts/auto flag）
- Modify: `frontend/src/lib/api-client.ts`（`apiDownload`：Bearer + blob + Content-Disposition）
- Test: 同上

**Interfaces (consumes T1; produces):**
```ts
type DesignGraph, DesignCaseDetail, DesignOverview, CaseConfirmationInput/Result, ExportView, ExportFormat = "markdown"|"mermaid"|"opml"|"csv"
listDesignConversations/createDesignConversation; getDesignGraph/getDesignOverview/getDesignCase; confirmCases; createExport/listExports/downloadExport
useVersionEvents handlers: onDesignGraphChanged/onDesignCaseChanged/onDesignRoundChanged/onDesignExportReady
```
- [ ] **Step 1: 写失败测试**
  `api.test.ts`：path/query/body、ID 字符串、错误透传；`apiDownload` 解析 filename、401 清 session、错误 envelope；`useVersionEvents` 路由 `design.graph_delta`/`design.case_version_*`/`design.case_confirmed`/`design.export_ready`。
- [ ] **Step 2: 运行失败**
  `cd frontend; pnpm exec vitest run src/features/design/api.test.ts`。
- [ ] **Step 3: 实现**
  所有请求走 `api-client`；SSE 走现有 `useVersionEvents`；不新增 EventSource。
- [ ] **Step 4: 通过**
  同一命令 + `pnpm typecheck`。
- [ ] **Step 5: Commit**
  `git add frontend/src/features/design frontend/src/features/clarification frontend/src/features/versions frontend/src/lib/api-client.ts && git commit -m "feat(p2): add design frontend API and SSE wiring"`

---

## Task 14: 脑图布局、节点、用例详情、DesignTab 壳（frontend）

**Files:**
- Create: `frontend/src/features/design/{layout.ts,DesignTab.tsx,DesignMindmap.tsx,RequirementNode.tsx,ScenarioNode.tsx,CaseCard.tsx,CaseDetailPanel.tsx}`
- Create tests: `layout.test.ts`、`DesignMindmap.test.tsx`、`CaseDetailPanel.test.tsx`、`DesignTab.test.tsx`
- Test: 同上

**Interfaces (consumes T13):** 确定性列布局 `layout(graph) -> {nodes,edges}`；React Flow custom node types；节点按钮回调用 `onConfirmCase/Scenario/Requirement`、`onSelectCase`；详情展示步骤/断言/来源/版本历史。
- [ ] **Step 1: 写失败测试**
  `layout.test.ts`：空图、1 需求/1 场景/1 用例、跨 SUT 卡片、200 用例上限坐标确定性；`DesignMindmap.test.tsx`：节点渲染、选中、缩放/适应按钮、确认按钮存在且仅 draft；`CaseDetailPanel.test.tsx`：步骤/断言/版本历史/`请求修改` 填入输入；`DesignTab.test.tsx`：空态/加载/错误/顶部统计。
- [ ] **Step 2: 运行失败**
  `cd frontend; pnpm exec vitest run src/features/design/layout.test.ts src/features/design/DesignMindmap.test.tsx src/features/design/CaseDetailPanel.test.tsx src/features/design/DesignTab.test.tsx`。
- [ ] **Step 3: 实现**
  `@xyflow/react` + 纯函数布局；节点位置不落库；顶部工具栏（导出/自动开关/新建对话占位插槽，组件在 T15）；无“AI 生成”按钮；无执行结果入口。Vitest 复用 `frontend/vitest.setup.ts` 已有的 React Flow polyfills（ResizeObserver/offsetWidth/DOMMatrix）。
- [ ] **Step 4: 通过**
  同一命令 + `pnpm typecheck`。
- [ ] **Step 5: Commit**
  `git add frontend/src/features/design && git commit -m "feat(p2): add design mindmap, case detail and tab shell"`

---

## Task 15: 设计聊天、导出对话框、自动开关、CTA（frontend）

**Files:**
- Create: `frontend/src/features/design/{DesignChat.tsx,ExportCasesDialog.tsx,AutoDesignSwitch.tsx,PendingDesignCTA.tsx}`
- Create tests: 对应 `*.test.tsx`
- Modify: `frontend/src/features/design/DesignTab.tsx`（插槽集成；与 T14 顺序执行）
- Test: 同上

**Interfaces (consumes T13/T14):** DesignChat 复用 `Composer`（parts: text/attachment）与 Markdown；TeamRoomPanel(team=design) 只读；ExportCasesDialog 4 格式；AutoDesignSwitch PATCH version；PendingDesignCTA 显示 `pending_design_count`。
- [ ] **Step 1: 写失败测试**
  `DesignChat.test.tsx`：历史/新建/发送/附件/SSE 消息/空态/错误；`ExportCasesDialog.test.tsx`：4 格式、POST、下载、失败重试；`AutoDesignSwitch.test.tsx`：默认关、PATCH、失败回滚；`PendingDesignCTA.test.tsx`：计数/跳转/无对话显示。
- [ ] **Step 2: 运行失败**
  `pnpm exec vitest run src/features/design/DesignChat.test.tsx src/features/design/ExportCasesDialog.test.tsx src/features/design/AutoDesignSwitch.test.tsx src/features/design/PendingDesignCTA.test.tsx`。
- [ ] **Step 3: 实现**
  复用 P1 `Composer`/`RichText`/`TeamRoomPanel`/`api-client`；聊天高度可滚动；无“结束本轮”按钮；导出按钮格式对话框。
- [ ] **Step 4: 通过**
  同一命令 + `pnpm typecheck`。
- [ ] **Step 5: Commit**
  `git add frontend/src/features/design && git commit -m "feat(p2): add design chat, export dialog and design toggles"`

---

## Task 16: VersionDetailPage/DesignCTA/深链集成（frontend）

**Files:**
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（design Tab 渲染 DesignTab；`?case=`/`?conversation=`）
- Modify: `frontend/src/features/clarification/DesignCTA.tsx`（可用链接/CTA）
- Modify: `frontend/src/features/clarification/RequirementsTab.tsx`（传递 count/跳转）
- Tests: `VersionDetailPage.test.tsx`、`DesignCTA.test.tsx`、`RequirementsTab.test.tsx`
- Test: 同上

- [ ] **Step 1: 写失败测试**
  design Tab 渲染 `design-tab`；`?tab=design&case=` 选中；CTA 可点击跳转；P1 clarification 不回归；占位文案移除。
- [ ] **Step 2: 运行失败**
  `pnpm exec vitest run src/features/versions/VersionDetailPage.test.tsx src/features/clarification/DesignCTA.test.tsx src/features/clarification/RequirementsTab.test.tsx`。
- [ ] **Step 3: 实现**
  路由仍由 `src/app` 组装；无新页面路由；`src/app` 不改。
- [ ] **Step 4: 通过**
  同一命令 + `pnpm lint && pnpm typecheck && pnpm test`（全前端）。
- [ ] **Step 5: Commit**
  `git add frontend/src/features && git commit -m "feat(p2): wire design tab and design CTA"`

---

## Task 17: server/main 装配与 server 集成测试（Lead）

**Files:**
- Modify: `backend/cmd/eztest/main.go`
- Modify: `backend/internal/server/{server.go,server_integration_test.go}`
- Test: `backend/internal/server/server_integration_test.go`

**Interfaces (consumes T5-T12):** `server.Deps` 增加 `DesignGraph *designapp.GraphService`、`DesignExports *designapp.ExportService`；server 注册 `designapi.NewHandler(DesignGraph, DesignExports)` 路由；main 构造 design 服务（repo/adapters）、storage `ExportService`、design enqueuer/team room/version adapters、`DesignStarter`、`DesignMetrics`、stale hook、agent tool DesignPort、agent `DesignReconciler`；替换 `NoopCaseStaleHook`。
- [ ] **Step 1: 写失败集成测试**
  `TestP2DesignEndToEnd`：创建 SUT/环境/页面/API/版本/需求→设计对话→（fake LLM）等图→确认→导出→下载；断言事件；断言版本详情 `pending_design_count`/`case_counts`。
- [ ] **Step 2: 运行失败**
  `powershell.exe -File scripts/test-integration.ps1 ./internal/server -run TestP2DesignEndToEnd -v` → 失败（路由/服务未装配）。
- [ ] **Step 3: 装配**
  唯一组装根；后台 worker/reconciler 随进程启停；不新增常驻 goroutine（design reconciler 由既有 agent reconciler 调用）。
- [ ] **Step 4: 通过**
  同一命令 + `go build ./...`。
- [ ] **Step 5: Commit**
  `git add backend/cmd backend/internal/server && git commit -m "feat(p2): wire design services into server and main"`

---

## Task 18: API 验收（fake）与浏览器 E2E（fake）（Lead）

**Files:**
- Create: `scripts/p2-acceptance.ps1`（复制 p1 结构：隔离库、fake LLM、API 14333、构建 exe、日志）
- Create: `scripts/p2-e2e.ps1`（Playwright；fake LLM 13335、Web 16868、截图）
- Test: 两脚本本身

**验收步骤（脚本内断言）**：
1. 登录/管理员配置 LLM（fake）→ SUT/环境/页面/API → 版本。
2. 创建/确认需求（走 API）。
3. 设计对话 → planner/author 任务 → `design.graph_delta` + 图含场景/用例 draft。
4. 对话发送“新增/修改用例” → Host propose → 新 draft/更新（无重复 case）。
5. 单用例/场景/需求确认 → 状态/事件/幂等；AI 无 confirm 工具。
6. 需求 PATCH → stale + `affected_case_version_ids`；重设计→新场景/用例；确认→旧 superseded。
7. 4 格式导出 + 下载 + sha/size/内容断言；重复导出新 revision。
8. D25：无设计对话 CTA/pending；已有对话自动并入；auto 开关自动建对话。
9. 跨 SUT 用例步骤目标校验；附件上传/读取。
10. SSE 事件快照断言；无 console error（E2E）。

- [ ] **Step 1: 写脚本**（先按预期断言；未实现则 BLOCKED/非 0）
- [ ] **Step 2: 运行失败**
  `powershell.exe -File scripts/p2-acceptance.ps1 -FakeLLMPort 13334` → 非 0。
- [ ] **Step 3: 修到通过**（只在脚本/测试辅助，不改产品逻辑绕过）
- [ ] **Step 4: E2E**
  `powershell.exe -File scripts/p2-e2e.ps1` → PASS；截图 `.tmp/p2-acceptance/screenshots/`。
- [ ] **Step 5: Commit**
  `git add scripts/p2-acceptance.ps1 scripts/p2-e2e.ps1 && git commit -m "test(p2): add P2 API acceptance and browser E2E"`

---

## Task 19: 真实 LLM 探针、API 验收、浏览器主链路（Lead）

**Files:**
- Create: `scripts/p2-acceptance-llm.ps1`（`-ProbeOnly` 参数）
- Create: `scripts/p2-e2e-llm.ps1`
- Test: 两脚本

**要求**：读取 `.env` 小写 `llm_*`（不新增 EzTest env）；经 admin API 写 `llm_config`/refresh；真实设计对话→产图→对话修改→确认→导出；语义断言（覆盖需求、无重复 case、状态正确、工具调用合法）；超时/重试；浏览器主链路真实 LLM；证据 `.tmp/p2-acceptance/{llm-probe.log,llm.log,e2e-llm/}`。缺 key/失败 → 输出 BLOCKED/非 0，handoff 记录，不以 fake 替代。

- [ ] **Step 1: 实现探针** `p2-acceptance-llm.ps1 -ProbeOnly` → 真实模型至少产出 1 场景/1 用例（可重试 2 次）。
- [ ] **Step 2: 运行探针** → PASS；失败则先修 schema/prompt（不改 spec 语义）。
- [ ] **Step 3: 实现全链路** 真实 API 验收 + 真实浏览器。
- [ ] **Step 4: 运行** `p2-acceptance-llm.ps1`、`p2-e2e-llm.ps1` → PASS；记录日志/截图。
- [ ] **Step 5: Commit**
  `git add scripts/p2-acceptance-llm.ps1 scripts/p2-e2e-llm.ps1 && git commit -m "test(p2): add real LLM acceptance and browser path"`

---

## Task 20: 契约冻结、全量 gate、handoff（Lead）

**Files:**
- Modify: `docs/version-V0.2/contracts/README.md`（P2 frozen + 证据指针）
- Create: `docs/version-V0.2/phases/phase-02-handoff.md`（按 handoff 模板）
- Test: 全量命令

**步骤**
- [ ] **Step 1: 冻结标记**：openapi 根标记 `x-draft: P2` → `x-frozen: P2`；其他契约 `frozen: P2`；更新 `scripts/p2-contract-check.cjs` 断言 frozen；检查无 P0/P1 破坏性 diff。
- [ ] **Step 2: 全量门禁** `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → EXIT=0；日志 `.tmp/p2/final-gate.log`。
- [ ] **Step 3: 契约检查** `node scripts/p2-contract-check.cjs` → 0；YAML 解析；UTF-8 扫描。
- [ ] **Step 4: 验收证据**：API 验收、E2E、真实 LLM 探针/API/浏览器全绿；整理 `.tmp/p2-acceptance/`。
- [ ] **Step 5: 写 handoff**：完成范围/契约变更/证据表/缺口/下一阶段输入（P3 的 current+confirmed、design.gap、目标字段）/风险；提示词映射更正；Agent.md §6.1 更新项；P2-D 裁决列表。
- [ ] **Step 6: Commit** `git add docs/version-V0.2 && git commit -m "docs(p2): freeze contracts and complete handoff"`。

---

## Task 21: 独立 whole-branch review 与修复（reviewer + Lead）

**Files:** 全分支 diff；`.tmp/p2/review.md`。

- [ ] **Step 1: 生成 review package** `git merge-base feat/v0.2-p1 HEAD`..HEAD（diff + stat + log）写入 `.tmp/p2/review-package.diff`。
- [ ] **Step 2: 派发独立 reviewer**（fresh subagent，read-only）：按 spec/plan/Review Focus 检查契约、状态机、并发、安全（AI 不能确认/不能删除）、测试真实性、无 0.1 回流；输出 Critical/Important/Minor + 文件:行。
- [ ] **Step 3: 修复** Critical/Important：由对应 workstream 实现者或 Lead 修复；重跑覆盖测试；每轮 scoped re-review。
- [ ] **Step 4: 复跑全量 gate + 验收**；review clean 后写 review disposition 到 handoff。
- [ ] **Step 5: Commit** 修复与 handoff 更新。

---

## Self-Review（写完计划后自查，已在本文件内修正）

- **Spec coverage**：§3 表/迁移→T1/T2/T3/T4；§4 状态/确认→T5/T6；§4.6 D25→T7/T12；§5 Agent→T11；§6 对话/事件→T12/T17；§7 API→T8/T17；§8 导出→T9/T10/T18；§9 planner→T7/T17；§10 前端→T13-T16；§11 契约→T1/T20；§12 测试→T18/T19/T20；§14 裁决→各任务 + T20。
- **Step scan**：每步可执行/可验证；无 TBD；测试先写、失败、实现、通过、提交。
- **Type consistency**：`designapp.GraphService/Planner/Exporter`、`tools.DesignPort`、`versionapp.DesignStarter/DesignMetrics/CaseStaleHook`、`ConversationPort.Kind`、`WorkItemPayload` 字段在各任务命名一致。
- **Review Focus**：五类均绑定测试（契约检查、跨 SUT、去重/hash、确认/stale、旧假设）。
- **Proportion**：计划给出接口/测试/文件，不写完整实现；实现者可读 spec + 现有代码。
- **Known residual**：真实 LLM 非确定性；计划用语义断言/重试/超时，真实失败阻断 handoff；fake 不作为质量证据。
