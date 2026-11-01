# EzTest 0.2 P8（硬化与验收）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在真实 LLM/Chrome/双 SUT/双副本下完成 0.2 全量旅程与 P9 回归，修复 P7 遗留的 11 项硬化缺口，冻结 P8 契约，完成 37 行能力矩阵签字与根文档 0.2 升级，交付可复现验收证据与 handoff。

**Architecture:** 先由 Lead 建迁移/设置/契约骨架；W1 六路并行实现后端硬化（各自包内 TDD，不碰组装根）；W2 并行交付验收脚本、E2E、能力矩阵与文档；W3 由 Lead 统一 wiring、server 中间件、契约冻结、真实环境验收、修复波与 handoff。验收采用模块化脚本（全旅程/P9/长跑/多副本/能力/E2E/静态）+ 聚合器。

**Tech Stack:** Go 1.24 + PostgreSQL 17 + Next.js/React + Node/Playwright executor + 独立 PDF renderer + PowerShell 验收脚本。

**Spec:** `docs/superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md`

## Global Constraints

- 分支 `feat/v0.2-p8`（从 `feat/v0.2` @`e2cf110`）；完成合回 `feat/v0.2`。
- 不修改 `docs/version-V0.2` 设计文档（architecture/high-level/research/ui/README/phases 非本阶段文件）；只改 handoff、contracts、迁移、验收证据与根文档。
- 不恢复 0.1 控制面假设；0.1 脚本（`acceptance.ps1`/`slo.ps1`/`security-audit.ps1`/`build-release.ps1`/`acceptance-p8-llm.ps1` 等）不得被 CI 或 P8 脚本引用。
- `.env` 严格 4 项：`EZTEST_DATABASE_URL`、`EZTEST_API_PORT`、`EZTEST_WEB_PORT`、`EZTEST_EXECUTOR_PORT`；其余进 DB 设置中心。
- 雪花 ID `BIGINT`；JSON/URL 用十进制字符串；跨上下文只逻辑引用、不建 FK。
- 契约只追加/修复，不破坏 P0–P7 语义；P8 冻结 `0.2.0-p8` / `x-frozen: P8`。
- 测试证据必须含命令、环境、数据、时间戳、退出码；不得只写“已通过”。
- 验收不得改产品代码/直接写 DB 绕过失败；发现缺陷记 FAIL，修复后复跑。
- 默认不新增 work_item 类型/profile/工具；不新增 API path/operation/schema。
- 限流默认 `0`（关闭）；事件保留 `retention.event_days=90`；审计保留 `security.audit_retention_days=90`。
- SLO 测量、安全审计、fake-LLM 回归、性能基准、Docker 实跑：**不做**（用户批准，handoff 记录偏离）。
- 用户未跟踪文件 `docs/用户专用文件，禁止AI阅读.md`：不读、不改、不提交。

## Review Focus

1. **真实 LLM 非确定性**：澄清/设计 run 可能不产出结构合法的需求/用例；验收必须通过结构化提交 + 服务端校验，失败记产品缺陷，不得用 fake 数据或直接写 DB 代理。
2. **多副本竞争**：双副本首启、迁移、leader 接管、drain 窗口可能重复派发/重复迁移；所有断言必须计数而非“看起来正常”。
3. **压缩丢事实**：长会话/长执行必须验证滚动摘要、pinned facts、`context.compacted` 与原始证据可展开；`context_overflow` 不得成为终态失败。
4. **清理/回放断档**：事件/审计按 `created_at` 批量删除；旧游标回放必须按既有 `stream.reset` 语义，不得 500/静默空。
5. **多执行器错投**：`owner_executor_id`、`case_run_sut_context.executor_id`、`version_sut_id` 与证据归属必须一致；单端点模式必须完全回归。

---

## 执行模型与写作用域

| 波次 | 任务 | 写作用域 |
|---|---|---|
| W0（Lead 串行） | Task 1–3 | `backend/internal/db/migrations/**`、`settings/application/catalog.go`、`contracts/**`、`scripts/p8-contract-check.cjs`、`scripts/p7-contract-check.cjs` |
| W1（6 路并行） | Task 4–9 | Task 4 `backend/internal/{settings,eventing}/**`；Task 5 `backend/internal/{orchestration,server}/**` + `platform/load/**`；Task 6 `backend/internal/{agent,knowledge}/**`；Task 7 `backend/internal/execution/**`；Task 8 `backend/internal/{delivery/infrastructure/renderclient,platform/id,cmd/eztest/main.go}`；Task 9 `backend/internal/{audit,eventing,server}/**`；均含同目录 `_test.go`，不碰 `cmd/eztest` wiring |
| W2（并行） | Task 10–16 | 各自新脚本/新文档/E2E 文件 |
| W3（Lead 串行） | Task 17–21 | `cmd/eztest/**`、`server/server.go`、`contracts/**`、验收证据、handoff |

规则：共享文件（contracts、`cmd/eztest/main.go` 除 Task 8 的版本一行、`server.New`、`p7-contract-check.cjs`、根 README）由 Lead 收口；子代理不得并发改同一文件。W1 任务只实现包内能力 + 单测/集成测试；wiring 统一在 Task 17。W2 中 `scripts/p8-fixtures.ps1` 由 Task 10 独占；Task 11–14 只消费其接口，需要新增 helper 时由 Task 10 负责人小步追加（串行），不得并发改同一文件。

---

# W0（Lead 串行）

### Task 1: 迁移 #41/#42 与设置两键

**Files:**
- Create: `backend/internal/db/migrations/orchestration_0002_instance_inflight.sql`
- Create: `backend/internal/db/migrations/execution_0005_case_run_sut_context_executor.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Modify: `backend/internal/settings/application/catalog.go`
- Test: `backend/internal/settings/application/catalog_test.go`, `backend/internal/db/migrate_test.go`, `backend/internal/db/migrate_integration_test.go`

**Interfaces:**
- Produces: `api_instance.active_requests/active_work INTEGER NOT NULL DEFAULT 0`；`case_run_sut_context.executor_id BIGINT NULL`；设置键 `security.rate_limit_per_minute`（number，默认 0，0–60000）、`retention.event_days`（number，默认 90，1–3650）。

- [ ] **Step 1: 写迁移 SQL**

`orchestration_0002_instance_inflight.sql`:

```sql
ALTER TABLE api_instance
    ADD COLUMN active_requests INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN active_work     INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT ck_api_instance_active_requests CHECK (active_requests >= 0),
    ADD CONSTRAINT ck_api_instance_active_work CHECK (active_work >= 0);
```

`execution_0005_case_run_sut_context_executor.sql`:

```sql
ALTER TABLE case_run_sut_context
    ADD COLUMN executor_id BIGINT,
    ADD CONSTRAINT ck_case_run_sut_context_executor CHECK (executor_id IS NULL OR executor_id > 0);
```

- [ ] **Step 2: 追加 `order.txt` 两行**（#41 `orchestration_0002_instance_inflight.sql`、#42 `execution_0005_case_run_sut_context_executor.sql`）

- [ ] **Step 3: 写失败测试**

`catalog_test.go`：期望定义总数 85；断言 `security.rate_limit_per_minute` 默认 `0`、`retention.event_days` 默认 `90`、类别分别 `security`/`retention`、均非 secret/非 restart。
`migrate_test.go`：`len(migrations) == 42`。`migrate_integration_test.go` 三处 40→42；新增 `TestP8MigrationsAddInstanceInflightAndSutContextExecutor`：查询 `information_schema.columns` 存在两列且约束存在。

- [ ] **Step 4: 运行测试确认失败**

Run: `cd backend && go test ./internal/settings/application ./internal/db`
Expected: FAIL（42/85/新键断言）。

- [ ] **Step 5: 实现设置定义并跑通**

在 `catalog.go` 追加两个 `settings.Definition`（类别/默认/范围/描述按 §7）。补齐 order.txt。

Run: `cd backend && go test ./internal/settings/application ./internal/db`
Expected: PASS；`go test -tags=integration ./internal/db` PASS。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/db/migrations backend/internal/settings/application/catalog.go backend/internal/settings/application/catalog_test.go backend/internal/db/migrate_test.go backend/internal/db/migrate_integration_test.go
git commit -m "feat(p8): add instance inflight columns, sut-context executor and retention/rate-limit settings"
```

---

### Task 2: P8 契约骨架与门禁

**Files:**
- Modify: `docs/version-V0.2/contracts/openapi.yaml`（`info.version`、`x-frozen`、InstanceView/ready 字段、429 说明、stale 文本）
- Modify: `docs/version-V0.2/contracts/db-schema.md`、`settings.md`、`migrations-order.md`、`events.md`、`executor-protocol.md`、`task-types.md`、`agent-profiles-tools.md`、`README.md`
- Create: `scripts/p8-contract-check.cjs`
- Modify: `scripts/p7-contract-check.cjs`（计数改 `>=`，不硬编码 P7 后总数）

**Interfaces:**
- Produces: `node scripts/p8-contract-check.cjs` exit 0；断言 `info.version=0.2.0-p8`、根 `x-frozen: P8`、162 paths/200 ops/264 schemas、迁移 42、设置 85、事件 118、任务 19、profiles 14、表 85、P2–P7 增量仍存在。

- [ ] **Step 1: 先写门禁脚本（会失败）**

`p8-contract-check.cjs`：读取 `openapi.yaml`/`db-schema.md`/`settings.md`/`migrations-order.md`/`events.md`/`task-types.md`/`agent-profiles-tools.md`，断言上述计数与版本；复用 P2–P7 的解析方式（Node 内置 + `frontend/node_modules/yaml`）。
`p7-contract-check.cjs`：把 40/83/162/200/264 等硬编码改为 `>=` 或“存在即可”，保留 P7 增量断言。

- [ ] **Step 2: 运行确认失败**

Run: `node scripts/p8-contract-check.cjs`
Expected: FAIL（版本仍 p7）。

- [ ] **Step 3: 更新契约文本**

- `openapi.yaml`：version/frozen；`InstanceView` 加 `active_requests`/`active_work`（integer，默认 0）；ready data 加 `inflight_requests`/`inflight_work`；通用错误加 429 说明；修复文件级 `x-frozen: P2`/P3 draft 注释与“P3 只有一个 endpoint”等 stale 文本。
- `db-schema.md`：P8 §17（两列）；表数 85；迁移 #41/#42。
- `settings.md`：§4h（83→85，2 键）。
- `migrations-order.md`：§10（#41/#42，order.txt 42）。
- `events.md`：§17 审计 action `retention.cleanup`；无新事件。
- `executor-protocol.md`：§12.3 多端点派发改 P8 已实现；线协议 v1.2 不变。
- `task-types.md`/`agent-profiles-tools.md`：去重复保留段；知识 query 派生说明。
- `README.md`：P8 冻结段与计数。
- 修正 settings §2 计数、§4g 热加载措辞、db §5 表数、task §4、exec §10 等既有矛盾。

- [ ] **Step 4: 跑门禁**

Run: `node scripts/p2-contract-check.cjs; node scripts/p3-contract-check.cjs; ...; node scripts/p7-contract-check.cjs; node scripts/p8-contract-check.cjs`
Expected: 全部 exit 0（PowerShell 用 `;` 逐个；记录输出）。

- [ ] **Step 5: 提交**

```bash
git add docs/version-V0.2/contracts scripts/p8-contract-check.cjs scripts/p7-contract-check.cjs
git commit -m "feat(p8): freeze p8 contract skeleton and add p8 contract gate"
```

---

### Task 3: 基线验证与记录

**Files:**
- Modify: `docs/superpowers/plans/2026-11-02-eztest-v02-p8-hardening-acceptance.md`（本计划，记录基线）
- Evidence: `.tmp/p8-baseline/`（日志）

**Steps:**
- [ ] 运行 `pwsh scripts/test.ps1`，记录 exit code、Go 包数、frontend test 数、失败项。
- [ ] 运行 `node scripts/p2..p7-contract-check.cjs`，记录计数。
- [ ] 运行 `cd frontend && node scripts/validate-compose.mjs`，记录 PASS。
- [ ] 把命令/输出路径/时间戳写入本计划末尾“基线记录”；任何失败按 spec §11.6 分级，P0/P1 先修再进入 W1。

---

# W1（6 路并行后端）

### Task 4: H1 跨实例设置缓存失效

**Files:**
- Modify: `backend/internal/eventing/application/listener.go`
- Modify: `backend/internal/eventing/infrastructure/listen.go`
- Test: `backend/internal/eventing/application/listener_test.go`、`backend/internal/eventing/infrastructure/listen_integration_test.go`
- Later wiring: `cmd/eztest/p8_wiring.go`（Task 17）

**Interfaces:**
- Produces:
  - `eventingapp.EventObserver func(context.Context, eventing.Event) error`
  - `eventingapp.WithDispatcherObserver(observer EventObserver) DispatcherOption`
  - `eventingpg.WithListenerObserver(observer eventingapp.EventObserver) ListenerOption`
- Consumes: `settings.Service.Reload(ctx) error`（已有）。

- [ ] **Step 1: 写失败测试**

`listener_test.go` 新增：
- `TestDispatcherObserverReceivesLoadedEvent`：`Handle` 后 observer 收到 `event.Type`；广播仍发生一次。
- `TestDispatcherObserverErrorDoesNotBreakBroadcast`：observer 返回 error 时 `Handle` 不返回该 error，广播仍发生一次。
- `TestDispatcherSkipsObserverForDuplicateNotification`：重复 payload 只观察一次。
`listen_integration_test.go` 新增 `TestListenerObserverFiresForCrossInstanceNotification`：A 发布、B 的 Listener observer 在 ≤3s 内收到同事件。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/eventing/...`
Expected: 编译失败（`WithDispatcherObserver` 未定义）。

- [ ] **Step 3: 实现**

`Dispatcher` 增加 `observer EventObserver`；`NewDispatcher(events, sink, options ...DispatcherOption)`；`Handle` 在 `sink.BroadcastLoaded(event)` 后调用 observer，observer error 原样返回给调用方（Listener 只 log）。`Listener` 把 `WithListenerObserver` 传入 `NewDispatcher`。不改变去重/NOTIFY 语义。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/eventing/... && go test -tags=integration ./internal/eventing/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/eventing
git commit -m "feat(p8): observe persisted events and reload settings across replicas"
```

---

### Task 5: H2 in-flight drain 计数

**Files:**
- Migration: 已由 Task 1 创建 #41
- Create: `backend/internal/platform/load/counter.go` + `counter_test.go`
- Modify: `backend/internal/orchestration/domain/instance.go`
- Modify: `backend/internal/orchestration/application/ports.go`、`service.go`、`service_test.go`、`*_fakes_test.go`
- Modify: `backend/internal/orchestration/infrastructure/postgres/repo.go`、`repo_integration_test.go`
- Modify: `backend/internal/orchestration/interfaces/httpapi/handler.go`、`handler_test.go`
- Modify: `backend/internal/server/middleware.go`（新增 `TrackInflight`）、`middleware_test.go`
- Modify: `backend/internal/agent/application/runner.go`、`runner_test.go`（新增 `RunnerDeps.Load *load.Counter`；run 执行入口 Inc、defer Dec；不改变 run 状态机）
- Later wiring: `cmd/eztest/p8_wiring.go`（Task 17）

**Interfaces:**
- Produces:
  - `load.Counter`：`Inc()`, `Dec()`, `Value() int64`（`atomic.Int64`）。
  - `orchestrationapp.LoadTracker interface { InflightRequests() int; InflightWork() int }`；`func (s *Service) SetLoadTracker(t LoadTracker)`。
  - `orchestration.Instance.ActiveRequests int`、`ActiveWork int`。
  - `Repository.Heartbeat(ctx, id int64, at time.Time, activeRequests, activeWork int) (Status, bool, error)`。
  - `orchestrationapp.Readiness.InflightRequests int`、`InflightWork int`；`readyView` 字段 `inflight_requests`/`inflight_work`。
  - `server.TrackInflight(counter *load.Counter) func(http.Handler) http.Handler`（跳过 `/healthz`、`/api/health*`、`/api/stream`）。
- Consumes: Task 1 的列。

- [ ] **Step 1: 写失败测试**

- `counter_test.go`：`TestCounterConcurrentIncDec` 100 goroutine，最终 0，峰值 >0。
- `orchestration/application/service_test.go`：`TestHeartbeatReportsLoadMetrics`（fake tracker 返回 3/2，heartbeat 调用记录 3/2）；`TestReadinessCarriesInflightCounts`。
- `repo_integration_test.go`：`TestHeartbeatPersistsInflightCounts`（写 5/4，List 读回 5/4）。
- `handler_test.go`：`TestReadyIncludesInflightFields`、`TestInstanceViewIncludesInflightFields`。
- `server/middleware_test.go`：`TestTrackInflightCountsConcurrentRequests`、`TestTrackInflightSkipsHealthAndSSE`。
- `agent/application/runner_test.go`：`TestRunnerIncrementsLoadDuringRun`（用一个 run 生命周期，fake LLM 挂起/返回后计数回 0）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/platform/load ./internal/orchestration/... ./internal/server ./internal/agent/application`
Expected: 编译失败/断言失败。

- [ ] **Step 3: 实现**

- `platform/load` 计数器。
- `Instance` 字段 + `Readiness` 字段。
- `Service` 增加 `load LoadTracker` 与 `SetLoadTracker`；`runHeartbeat` 每轮读取（nil 时 0/0）并传给 `repo.Heartbeat`。
- repo SQL：`SET heartbeat_at=$2, updated_at=$2, active_requests=$3, active_work=$4`；`instanceColumns` 加两列并更新 scan。
- handler DTO/视图加字段。
- `TrackInflight` 中间件：`Inc/defer Dec`，跳过 health/SSE。
- `agent.Runner` 增加可选 `load *load.Counter`，在 run 执行入口 `Inc`、`defer Dec`；不改变 run 状态机。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/platform/load ./internal/orchestration/... ./internal/server ./internal/agent/application && go test -tags=integration ./internal/orchestration/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/platform/load backend/internal/orchestration backend/internal/server/middleware.go backend/internal/server/middleware_test.go backend/internal/agent/application/runner.go backend/internal/agent/application/runner_test.go
git commit -m "feat(p8): count in-flight requests and work for drain/rolling upgrade"
```

---

### Task 6: H3 知识注入 FTS query

**Files:**
- Modify: `backend/internal/agent/application/knowledge.go`、`knowledge_test.go`、`runner.go`（调用点）、fakes/tests
- Modify: `backend/internal/agent/infrastructure/knowledgeadapter/adapter.go`、`adapter_test.go`
- Modify: `backend/internal/knowledge/application/service.go`（已支持 Query，必要时只补测试）
- Test: `backend/internal/knowledge/infrastructure/postgres/repo_integration_test.go`

**Interfaces:**
- Produces:
  - `agentapp.KnowledgeReader.RetrieveForVersion(ctx, versionID int64, systemKeys []string, query string) ([]KnowledgeRef, int, error)`
  - `agentapp.KnowledgeQuery(versionPack VersionPack) string`（纯函数：Title + Requirements 去重、空白归一、截断 512 字符）。
- Consumes: `knowledgeapp.InjectInput.Query`（已有）。

- [ ] **Step 1: 写失败测试**

- `knowledge_test.go`：`TestKnowledgeQueryUsesVersionTitleAndRequirementTitles`（标题+2 需求，断言 query 含三者、去重、≤512）；`TestKnowledgeQueryEmptyWhenNoText`；`TestKnowledgeForVersionPassesQuery`（fake reader 记录 query）。
- `adapter_test.go`：`TestRetrieveForVersionPassesQuery`（fake service 收到 query）。
- `repo_integration_test.go`：`TestInjectionRanksTitleMatch`（query="退款" 命中标题/正文含退款的 approved 项；无关项不命中；空 query 仍按 id DESC）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/agent/... ./internal/knowledge/...`
Expected: 编译失败（签名不匹配）。

- [ ] **Step 3: 实现**

- `knowledgeForVersion(ctx, versionPack)` 构造 query 并调用扩展后的接口。
- `adapter.RetrieveForVersion` 透传 `Query`。
- 更新所有 fake/测试桩签名。
- 不改注入预算/scope/source_stale/usage 语义。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/agent/... ./internal/knowledge/... && go test -tags=integration ./internal/knowledge/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent backend/internal/knowledge
git commit -m "feat(p8): derive knowledge FTS query from version and requirement titles"
```

---

### Task 7: H4 多执行器 per-executor 派发

**Files:**
- Migration: 已由 Task 1 创建 #42
- Modify: `backend/internal/execution/domain/case_run.go`（`CaseRunSUTContext.ExecutorID *int64`）
- Modify: `backend/internal/execution/application/case_run.go`（`BrowserContextSpec.ExecutorID`、`BrowserExecutor` 方法加 executorID、`CaseRunner.ensureBrowserContext` 写入）
- Modify: `backend/internal/execution/application/tools.go`、`visual_assert.go`、`human.go`、`recovery.go`（所有 `BrowserExecutor` 调用点改为带 executorID）
- Modify: `backend/internal/execution/application/scheduler.go`（owner_executor_id 写入/多端点选择已存在，补断言/必要修复）
- Modify: `backend/internal/execution/infrastructure/adapters/executor_adapter.go`
- Create: `backend/internal/execution/infrastructure/executor/resolver.go` + `resolver_test.go`
- Modify: `backend/internal/execution/infrastructure/executor/client.go`（如需 `BaseURL`/clone）
- Modify: `backend/internal/execution/infrastructure/postgres/case_run.go`（列 scan/insert/update）
- Modify: `backend/internal/execution/application/registry.go`（`Get(ctx,id)`）
- Tests: `case_run_test.go`、`adapters_test.go`、`registry_test.go`、`scheduler_fakes_test.go`、`case_run_integration_test.go`

**Interfaces:**
- Produces:
  - `executionapp.BrowserExecutor` 新签名：
    `CreateContext(ctx, spec BrowserContextSpec) (string, error)`（spec.ExecutorID）
    `Action(ctx, executorID int64, contextID, action string, args json.RawMessage) (BrowserActionResult, error)`
    `Cancel(ctx, executorID int64, contextID string) error`
    `Close(ctx, executorID int64, contextID string) error`
    `ListContexts(ctx, executorID int64) ([]BrowserContextRef, error)`
  - `executorinfra.Resolver`：`NewResolver(token string, timeout time.Duration) *Resolver`；`Client(endpoint string) *HTTPClient`（按 endpoint 缓存，线程安全）。
  - `executionapp.ExecutorRegistry` 增加 `Get(ctx, id int64) (ExecutorInstance, error)`（Registry 实现）。
- Consumes: Task 1 列；`Registry.Select` 已支持多端点。

- [ ] **Step 1: 写失败测试**

- `registry_test.go`：`TestRegistryGetByID`。
- `resolver_test.go`：`TestResolverCachesByEndpoint`、`TestResolverDistinctEndpoints`、`TestResolverRejectsInvalidEndpoint`。
- `adapters_test.go`：`TestBrowserAdapterRoutesByExecutorID`（两个 fake client，断言 create/action/cancel/close 到正确 client）。
- `case_run_test.go`：`TestEnsureBrowserContextRecordsExecutorID`、`TestCaseContextUsesOwnerExecutor`。
- `scheduler_fakes_test.go`：`TestSchedulerDistributesRunsAcrossExecutors`（两个 active 行，两个 run owner 不同）。
- `case_run_integration_test.go`（`-tags=integration`）：`TestCaseRunSUTContextPersistsExecutorID`；`TestSingleEndpointModeUnchanged`。
- `internal_handler_test.go`/现有测试更新新签名。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/execution/...`
Expected: 编译失败。

- [ ] **Step 3: 实现**

- `CaseRunner.ensureBrowserContext` 从 `caseRun.OwnerExecutorID` 取 id（nil/0 时回退 0，适配器回退主端点）。
- `case_run_sut_context` 写入/读取 `executor_id`。
- `BrowserAdapter` 持 `Resolver` + `ExecutorLookup`（`Registry.Get`）；`clientFor` 按 id→endpoint→client；找不到 id 回退主端点（单端点兼容）。
- `ListContexts`/恢复流程按 executor id 逐执行器调用；`Close` 用行里的 executor_id。
- `Registry.Get` 返回行或 `execution.ErrNotFound`。
- 保持 HTTP 协议/action/证据映射不变。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/execution/... && go test -tags=integration ./internal/execution/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution
git commit -m "feat(p8): route browser actions to the owning executor endpoint"
```

---

### Task 8: H5 PDF 上限 + H6 API 版本 + H8 雪花/实例 ID

**Files:**
- Modify: `backend/internal/delivery/infrastructure/renderclient/client.go`、`client_test.go`
- Modify: `backend/cmd/eztest/main.go`（仅 version 一行）、`backend/cmd/eztest/p5_wiring_test.go`、`backend/internal/server/server_p5_wiring_test.go`
- Modify: `backend/internal/platform/id/snowflake.go`、`snowflake_test.go`
- Test: `backend/internal/orchestration/application/service_test.go`（实例 ID 并发已有重试，补测试）

**Interfaces:**
- Produces: `renderclient.MaxPDFBytes = 10 << 20`；`version = "0.2.0-p8"`；雪花序列耗尽等待下一毫秒（不逻辑进位到未来）。

- [ ] **Step 1: 写失败测试**

- `renderclient/client_test.go`：`TestMaxPDFBytesMatchesContentLedger`（常量 == 10<<20）；`TestPDFAtLimitAccepted`、`TestPDFOverLimitRejected`（用 httptest 返回 10MiB/10MiB+1；后者断言 `ErrRenderRejected`）。
- `p5_wiring_test.go`/`server_p5_wiring_test.go`：期望 `0.2.0-p8`。
- `snowflake_test.go`：`TestNextWaitsForNextMillisecondWhenSequenceExhausted`（用可控 `now`，注入 5000 次；断言时间戳不超前、ID 唯一、单调）；`TestNextConcurrentUnique`（8 goroutine × 10k）。
- `service_test.go`：`TestStartRetriesInstanceIDCollision`（已有则确认）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/delivery/infrastructure/renderclient ./internal/platform/id ./cmd/eztest ./internal/server`
Expected: FAIL/编译失败。

- [ ] **Step 3: 实现**

- 常量 10 MiB；错误信息同步。
- main.go 版本改 `0.2.0-p8`；测试更新。
- 雪花：序列耗尽时 `for now <= lastMilli { runtime.Gosched()/time.Sleep(50µs); now = s.now() }`，再取 sequence=0；保留时钟回拨单调；加注释。
- 实例 ID 冲突重试已有（`insertInstance`），仅补测试；如测试暴露问题按最小修复。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/delivery/infrastructure/renderclient ./internal/platform/id ./cmd/eztest ./internal/server`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/delivery/infrastructure/renderclient backend/internal/platform/id backend/cmd/eztest/main.go backend/cmd/eztest/p5_wiring_test.go backend/internal/server/server_p5_wiring_test.go backend/internal/platform/id/snowflake_test.go
git commit -m "feat(p8): align pdf limit, bump api version, harden snowflake sequence"
```

---

### Task 9: H10 保留清理 + H11 限流

**Files:**
- Create: `backend/internal/audit/application/retention.go` + `retention_test.go`
- Modify: `backend/internal/audit/infrastructure/postgres/**`（`DeleteOlderThan`）
- Create: `backend/internal/eventing/application/retention.go` + `retention_test.go`
- Modify: `backend/internal/eventing/infrastructure/postgres.go`（`DeleteOlderThan`）
- Create: `backend/internal/server/ratelimit.go` + `ratelimit_test.go`
- Later wiring: `cmd/eztest/p8_wiring.go`（Task 17）

**Interfaces:**
- Produces:
  - `auditapp.RetentionRepository interface { DeleteOlderThan(ctx, cutoff time.Time, limit int) (int64, error) }`
  - `eventingapp.RetentionRepository interface { DeleteOlderThan(ctx, cutoff time.Time, limit int) (int64, error) }`
  - `auditapp.RetentionService`：`Run(ctx, auditCutoff, eventCutoff time.Time, batch int) (RetentionResult, error)`；`RetentionResult{EventsDeleted, AuditDeleted int64}`；内部调用 audit repo + eventing repo + audit.Record(`retention.cleanup`)。
  - `server.RateLimit(settings LimitSettings) func(http.Handler) http.Handler`；`LimitSettings interface { Int(ctx, key string) (int64, error) }`；429 JSON `{"error":{"code":"rate_limited",...}}` + `Retry-After`。
- Consumes: `security.audit_retention_days`、`retention.event_days`、`security.rate_limit_per_minute`、`retention.gc_batch_size`。

- [ ] **Step 1: 写失败测试**

- `audit/application/retention_test.go`：`TestRetentionRunDeletesBothAndAudits`（fake repos 返回 3/5，断言结果 + audit action）；`TestRetentionContinuesOnEventError`；`TestRetentionCutoffFromSettings`。
- `eventing/application/retention_test.go`：batch/cutoff/空结果。
- 集成（`-tags=integration`）：`TestRetentionDeletesOldEventsAndAuditRows`（backdate 旧行、留新行；断言旧删新留、审计行）。
- `server/ratelimit_test.go`：`TestRateLimitDisabledWhenZero`；`TestRateLimit429WithRetryAfter`（限 30，40 次请求，至少 1 个 429 且头存在）；`TestRateLimitSkipsHealthAndSSE`；`TestRateLimitSettingsReadPerRequest`。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/audit/... ./internal/eventing/... ./internal/server`
Expected: 编译失败。

- [ ] **Step 3: 实现**

- `DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE created_at < $1 ORDER BY created_at LIMIT $2)`；event 同法（索引已存在）。
- `RetentionService` 循环直到返回 < batch；记录 `retention.cleanup` 审计。
- `RateLimit`：每实例令牌桶（rate=limit/60，burst=max(limit/10,1)），每请求读 settings（缓存）；skip 路径；429 + `Retry-After: 1`；`limit<=0` 直接 next。
- 不新增事件。

- [ ] **Step 4: 跑测试**

Run: `cd backend && go test ./internal/audit/... ./internal/eventing/... ./internal/server && go test -tags=integration ./internal/audit/... ./internal/eventing/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/audit backend/internal/eventing backend/internal/server/ratelimit.go backend/internal/server/ratelimit_test.go
git commit -m "feat(p8): add audit/event retention cleanup and per-instance rate limiting"
```

---

# W2（并行交付）

### Task 10: 全旅程验收脚本 `p8-acceptance.ps1`

**Files:**
- Create: `scripts/p8-acceptance.ps1`
- Create: `scripts/p8-fixtures.ps1`（共享 helper：登录/HTTP/psql/等待/fixture 进程）
- Test: 运行脚本 S0–S6（首轮允许 FAIL，记录产品缺陷）

**Interfaces:**
- Consumes: p8 API（契约），fixture target（`FIXTURE_VARIANT=orders|payments`，端口 14080/14081），真实 LLM/executor/renderer。
- Produces: `.tmp/p8-acceptance/scenarios.txt`、`acceptance.log`、`api-*.log`、`executor*.log`、`s0..s6-*.json/html/pdf`；退出码 0 当且仅当所有 check PASS。

- [ ] **Step 1: 写 helper + S0**

参数：`-ApiPort 13333 -DatabaseName eztest_v02_p8 -SkipDatabaseReset -Scenarios @()`；启动 fixture/executor/renderer/API；S0 断言迁移 42/设置 85/LLM config/renderer/executor/ready/version。

- [ ] **Step 2: 写 S1–S2（管理员配置/版本绑定）**

通过 API 创建：2 SUT（family `p8-orders`/`p8-payments`）、环境、页面、API、目标 DB、账号、代码仓；创建版本并绑定全部；断言读回/权限/secret 掩码/默认 SUT。

- [ ] **Step 3: 写 S3–S4（真实 LLM 澄清/设计）**

新建澄清对话→≥3 轮→轮询 requirements draft→用户编辑/确认→断言版本/确认；新建设计对话→轮询 case/scenario→确认→断言 current+confirmed 与跨系统/失败用例存在。重试上限 2；失败记 FAIL 不伪造。

- [ ] **Step 4: 写 S5–S6（真实执行/交付/收尾/重开）**

确认→intents/wave→真实 executor 执行→轮询结果→断言证据/跨 SUT 上下文；暂停/恢复；失败→issue；波次/版本报告 HTML/PDF；链路节点/边；需求 goal_met；等待 Version Steward 收尾≤10min→completed；reopen→running。

- [ ] **Step 5: 跑 S0–S6 并记录**

Run: `pwsh scripts/p8-acceptance.ps1 -SkipDatabaseReset`
Expected: 首轮可 FAIL；把每个 FAIL 写入 `.tmp/p8-acceptance/bug-notes.txt` 并分级；修复后复跑。

- [ ] **Step 6: 提交**

```bash
git add scripts/p8-acceptance.ps1 scripts/p8-fixtures.ps1
git commit -m "test(p8): add full-journey real-LLM/Chrome acceptance driver"
```

---

### Task 11: 长跑/硬化定向脚本 `p8-resilience.ps1`

**Files:**
- Create: `scripts/p8-resilience.ps1`
- Modify: `scripts/fixture-target.cjs`（新增 `GET /p8/large?bytes=N` 大页面，仅测试夹具）
- Test: 运行 S9–S10

**Interfaces:**
- Consumes: p8 API/DB、双 executor、settings。
- Produces: `.tmp/p8-resilience/` 证据 + 退出码 0。

- [ ] **Step 1: S9 长上下文**

临时设 `context.window_tokens=8000`/`agent.tool_result_max_bytes=8192`；通过 API 预置 30+ 用户消息（含长文本）→触发澄清 run→断言 `conversation_summary`、pinned facts、`context.compacted` 事件、run 非 `context_overflow` 终态、原文可展开；恢复设置。

- [ ] **Step 2: S9 长执行**

API 创建 12 个确定性用例（含跨系统/慢页/大结果）→确认→2 波；暂停 1 个恢复；`/p8/large?bytes=20000` 触发 tool 结果落盘→断言 head+ref、检查点续跑、无重复步骤。

- [ ] **Step 3: S10 定向硬化**

设置热更（A 改 B 读 ≤2s）；多执行器各至少 1 run（owner 不同）；限流 30→429+Retry-After→恢复 0；保留清理旧删新留+`retention.cleanup` 审计；API 版本；雪花 10k 唯一（或单元证据）；PDF 10MiB 边界（单元证据引用）。

- [ ] **Step 4: 跑并记录**

Run: `pwsh scripts/p8-resilience.ps1`
Expected: 首轮可 FAIL；修复后 PASS；证据 `.tmp/p8-resilience/`。

- [ ] **Step 5: 提交**

```bash
git add scripts/p8-resilience.ps1 scripts/fixture-target.cjs
git commit -m "test(p8): add long-context/long-execution and hardening acceptance"
```

---

### Task 12: 多副本脚本 `p8-multi.ps1`

**Files:**
- Create: `scripts/p8-multi.ps1`
- Test: 运行 S8

**Interfaces:**
- Consumes: p8 库、双 API、共享 `.data`、双 executor、真实 Chrome。
- Produces: `.tmp/p8-multi/` 证据 + 退出码 0。

- [ ] **Step 1: 双副本启动/就绪/设置热更**

起 A/B（node 1/2，共享库/根）；both ready；A 更新设置→B ≤2s 读到；`/api/admin/instances` 显示两行与计数。

- [ ] **Step 2: 滚动升级/drain 计数**

B ready→drain A→A readiness 503→记录 `active_requests`/`active_work` 下降→归零→停 A→B 接管 leader；断言无重复 task/agent_run/case_run、迁移只一次。

- [ ] **Step 3: 故障/SSE/真实离线重派**

kill -9 A（或 B）→存活副本接管；跨实例 SSE 发布/回放；executor B 跑慢页时停止→case_run `interrupted(executor_lost)`→`dispatch_epoch++`→重派 A 成功；同波其他 run 不受影响。

- [ ] **Step 4: 跑并记录**

Run: `pwsh scripts/p8-multi.ps1`
Expected: PASS；证据 `.tmp/p8-multi/`。

- [ ] **Step 5: 提交**

```bash
git add scripts/p8-multi.ps1
git commit -m "test(p8): add rolling upgrade, leader takeover and real executor-loss drill"
```

---

### Task 13: 能力矩阵 smoke + 签字文档

**Files:**
- Create: `scripts/p8-capability.ps1`
- Create: `docs/version-V0.2/acceptance/p8/capability-signoff.md`
- Test: 运行 S11

**Interfaces:**
- Consumes: 37 行矩阵（`research/capability-matrix.md`）、P6 `capability-parity.md`、P7 acceptance。
- Produces: 37 行每行 PASS/PASS(prior)/FAIL + 证据指针；脚本退出码 0。

- [ ] **Step 1: 写 37 行检查表**

每行：编号、能力、入口（API/UI）、P8 检查（HTTP 状态/路由存在/数据断言）、既往证据路径。P8 未覆盖行（如 B4/D3/G2）标 `PASS(prior)` 并给 P5/P6 证据。

- [ ] **Step 2: 写脚本**

登录管理员/普通用户；逐行执行 HTTP/psql/文件存在检查；输出表格与失败项；对 P9 行引用 `p7-acceptance` 证据。

- [ ] **Step 3: 生成签字文档**

`capability-signoff.md`：37 行表格 + 结论 + 签字（Lead 代用户）+ 未覆盖说明。

- [ ] **Step 4: 跑并记录**

Run: `pwsh scripts/p8-capability.ps1`
Expected: 37/37 PASS 或 PASS(prior)；FAIL 0。

- [ ] **Step 5: 提交**

```bash
git add scripts/p8-capability.ps1 docs/version-V0.2/acceptance/p8/capability-signoff.md
git commit -m "test(p8): add capability matrix smoke and sign-off evidence"
```

---

### Task 14: 浏览器 E2E `p8.spec.ts` + 脚本

**Files:**
- Create: `frontend/e2e/p8.spec.ts`
- Create: `scripts/p8-e2e.ps1`
- Modify: `frontend/e2e/p6.spec.ts`、`p7.spec.ts`（动态发现 ID）
- Test: 运行 `p8-e2e.ps1`

**Interfaces:**
- Consumes: p8 库（S0–S6 数据）、API/Web、真实 Chrome。
- Produces: `.tmp/p8-e2e/playwright.log`、`docs/version-V0.2/acceptance/p8/screenshots/p8-e2e-*.png`（≥20）、`e2e.md`。

- [ ] **Step 1: 修 p6/p7 固定 ID**

`beforeAll` 登录后通过 API 查找版本/报告/知识/问题；保留 `EZTEST_E2E_*` 覆盖；删除硬编码默认 ID。

- [ ] **Step 2: 写 p8 spec**

场景：`/resources` 资源 Tab→`/versions` 新建/打开版本→requirements（对话/确认卡）→design（脑图/确认）→execution（波次/证据/暂停）→issues（重开/合并链）→reports（PDF/HTML）→chain（过滤/深链）→版本重开；全局 `/knowledge` `/quality` `/agents` `/issues` `/reports` `/settings`。截图 fullPage/viewport/element 三 helper，编号 `p8-e2e-NN-name.png`。

- [ ] **Step 3: 写脚本**

起 API/Web（复用 p7-e2e 模式）；跑 `pnpm exec playwright test e2e/p8.spec.ts --reporter=line`；拷贝截图；写 `e2e.md`（日期/环境/命令/结果表/截图/证据/数据变异/缺陷）。

- [ ] **Step 4: 跑并记录**

Run: `pwsh scripts/p8-e2e.ps1`
Expected: PASS；≥20 截图；无固定 seed 失败。

- [ ] **Step 5: 提交**

```bash
git add frontend/e2e scripts/p8-e2e.ps1 docs/version-V0.2/acceptance/p8
git commit -m "test(p8): add dynamic-seed browser e2e for the full journey"
```

---

### Task 15: 根文档 0.2 升级与 0.1 归档

**Files:**
- Modify: `README.md`、`README.zh-CN.md`、`DEVELOPMENT_STATUS.md`
- Create: `docs/version-V0.1/README.md`、`docs/version-V0.1/DEVELOPMENT_STATUS.md`
- Modify: `docs/architecture-design.md`、`detailed-design.md`、`deployment.md`、`operations.md`、`user-guide.md`、`development.md`、`docs/openapi/api.yaml`、根 `acceptance.md`（仅顶部归档横幅）
- Create: `docs/version-V0.2/acceptance/p8/runbook.md`
- Modify: `deploy/README.md`

**Steps:**
- [ ] **Step 1: 写 0.2 README（英/中）**：状态 v0.2（P8）、能力（0.1 P0–P8 对齐 + P9）、8 全局壳/8 Tab、4 项 env、快速启动（`scripts/dev-all.ps1`/`scripts/test.ps1`）、验收状态（P8 证据路径）、已知限制（Docker 未实跑、SLO/安全审计按用户裁定未做）、文档地图、License；不写无法验证的数字。
- [ ] **Step 2: 新 DEVELOPMENT_STATUS**：P0–P8 简表、测试/契约/迁移/设置计数、P8 验收状态；旧 100KB 移入 `docs/version-V0.1/DEVELOPMENT_STATUS.md`；`docs/version-V0.1/README.md` 归档索引。
- [ ] **Step 3: 0.1 文档横幅**：顶部加“0.1 归档；0.2 见 docs/version-V0.2/、deploy/README.md”；不重写正文；`docs/openapi/api.yaml` 顶部 YAML 注释。
- [ ] **Step 4: runbook**：0.2 本地开发/验收/多副本运行入口（命令、端口、env、验收脚本顺序、已知限制）；deploy/README 补 P8 实例计数/限流/清理说明。
- [ ] **Step 5: 检查链接/编码**

Run: `pwsh scripts/p8-static-check.ps1`（Task 16）或临时链接脚本；`git diff --check`。
Expected: 无坏链/无 0.1 状态残留。

- [ ] **Step 6: 提交**

```bash
git add README.md README.zh-CN.md DEVELOPMENT_STATUS.md docs deploy/README.md
git commit -m "docs(p8): upgrade product docs to 0.2 and archive 0.1"
```

---

### Task 16: 静态检查 + CI 去 0.1 化

**Files:**
- Create: `scripts/p8-static-check.ps1`
- Modify: `.github/workflows/ci.yml`（static-acceptance job）
- Test: 本地运行 + 故意违规自测

**Interfaces:**
- Produces: `pwsh scripts/p8-static-check.ps1` exit 0；检查 4 env、openapi p8、settings 85、migrations 42、compose 6 服务、根 README 0.2、0.1 资产未被引用、无 secret、`.env` 未跟踪、文档链接。

- [ ] **Step 1: 写脚本**

读取 `.env.example`、`contracts/openapi.yaml`、`settings@catalog`（或 settings.md）、`order.txt`、`deploy/docker-compose.yml`、根 README/DEVELOPMENT_STATUS、`docs/version-V0.2` 文档链接；调用 `frontend/scripts/validate-compose.mjs` 与 `node scripts/p8-contract-check.cjs`；扫描 `scripts/*.ps1`/CI 中的 0.1 门禁引用；对失败给可操作错误。

- [ ] **Step 2: 跑确认失败/修复**

Run: `pwsh scripts/p8-static-check.ps1`
Expected: 首次可 FAIL（旧 README/CI）；修复后 PASS。

- [ ] **Step 3: 改 CI**

`static-acceptance` job：checkout→setup pnpm→`pnpm install --frozen-lockfile`（frontend）→`pwsh -File scripts/p8-static-check.ps1`；删除 `check-ci.ps1`/`acceptance.ps1 -StaticOnly`。test job 不变。

- [ ] **Step 4: 自测**

临时改 openapi 版本/README 状态，确认脚本 exit 1；恢复后 exit 0。

- [ ] **Step 5: 提交**

```bash
git add scripts/p8-static-check.ps1 .github/workflows/ci.yml deploy/README.md
git commit -m "ci(p8): replace 0.1 static gate with p8 static checks"
```

---

# W3（Lead 串行）

### Task 17: 组装根 wiring 与中间件

**Files:**
- Create: `backend/cmd/eztest/p8_wiring.go`
- Modify: `backend/cmd/eztest/main.go`
- Modify: `backend/internal/server/server.go`（RateLimit/TrackInflight/health fields）
- Modify: `backend/internal/server/health.go`（如需要）
- Modify: `frontend/src/features/settings/**`（实例面板显示计数）
- Test: `backend/cmd/eztest/p8_wiring_test.go`、`server_p8_wiring_test.go`、frontend 单测

**Steps:**
- [ ] 创建 settings listener observer：`settings.updated` → `settingsSvc.Reload`；错误 log。
- [ ] 创建 `load.Counter` 两个实例（requests/work），接入 server 中间件、agent Runner、orchestration `SetLoadTracker`。
- [ ] 创建 executor `Resolver` + 完整 `Registry`，替换 `primaryExecutorRegistry`；`BrowserAdapter` 使用 resolver。
- [ ] 创建 retention worker：leader 日循环（复用 `retention.gc_interval_minutes`/`gc_batch_size`），调用 `RetentionService.Run`。
- [ ] `server.New` 链加 `TrackInflight`/`RateLimit`；health/Deps 传 load。
- [ ] 实例面板显示 `active_requests`/`active_work`；前端单测。
- [ ] 跑 `go test ./...` + `pnpm test`；修复所有 wiring 测试。

### Task 18: 契约冻结与门禁

**Files:** `contracts/**`、`scripts/p7-contract-check.cjs`、`scripts/p8-contract-check.cjs`、`contracts/README.md`

- [ ] 最终计数/版本核对（42/85/118/19/14/162/200/264）。
- [ ] P7 检查前向兼容；P8 检查通过。
- [ ] 契约文本矛盾修复完毕；`p8-contract-check.cjs` exit 0。
- [ ] 提交 `feat(p8): freeze p8 contracts`。

### Task 19: 真实环境全量验收

**Files:** `scripts/p8-all.ps1`（聚合器）、`.tmp/p8-acceptance/**`、`docs/version-V0.2/acceptance/p8/**`

- [ ] 启动便携 PostgreSQL；确认 `.env`/真实 LLM/Chrome。
- [ ] 运行 `p7-acceptance.ps1`（重建 p7，13 场景）；记录证据。
- [ ] 克隆 p7→p8；跑 `p8-all.ps1`（S0–S13：acceptance/resilience/multi/capability/e2e/static）。
- [ ] 失败项分级、修复、复跑；每轮记录时间/退出码/check 数。
- [ ] 验证 P8 五条成功标准与 37 行矩阵。

### Task 20: 验收文档/证据/handoff/合并

**Files:** `docs/version-V0.2/acceptance/p8/{acceptance,resilience,multi,e2e,static,known-limitations}.md`、`phase-08-handoff.md`、`DEVELOPMENT_STATUS.md`（如需）

- [ ] 写聚合 `acceptance.md`（命令/环境/场景/check/证据/缺陷/复跑）。
- [ ] 写 `known-limitations.md`（Docker 未实跑、SLO/安全审计按用户裁定未做、限流默认关、其他 P2/P3）。
- [ ] 写 `phase-08-handoff.md` 六节 + 最终交付说明 + 0.3 backlog + 运行/部署入口。
- [ ] 最终 `scripts/test.ps1`、全部契约、静态、E2E、验收复跑全绿。
- [ ] 合并回 `feat/v0.2`（merge commit）；停止并报告，不自动进入 0.3。

### Task 21: 独立评审与修复

- [ ] 用 fresh reviewer（subagent）按 spec/plan/验收证据做只读评审，输出 Critical/Important/Minor。
- [ ] 修复 Critical/Important；复跑相关门禁；Minor 记录。
- [ ] 更新 handoff 评审记录。

---

## 自审记录

- **Spec coverage**：H1–H11 → Task 4–9/17；S0–S13 → Task 10–14/19；契约 → Task 2/18；文档 → Task 15；CI → Task 16；矩阵 → Task 13；缺陷门禁 → Task 19/20。
- **Step scan**：每个后端任务有失败测试/实现/通过/提交；脚本/文档任务有可运行检查与证据路径。
- **类型一致性**：`LoadTracker`/`load.Counter`、`Heartbeat` 新签名、`BrowserExecutor` 新签名、`RetentionService.Run`、`RateLimit` 在 Task 4–9 定义，Task 17 消费，未出现别名。
- **Review Focus**：真实 LLM（S3/S4 重试+缺陷记录）、多副本竞争（S8 计数）、压缩丢事实（S9）、清理回放（S10）、多执行器错投（S10/S8 归属断言）均有任务覆盖。
- **Proportion**：计划以签名/测试/场景为主，不写实现正文。

## 基线记录（Task 3 填写）

- 日期/分支/HEAD：
- `scripts/test.ps1`：
- 契约 p2–p7：
- compose 校验：
- 失败/分级：
