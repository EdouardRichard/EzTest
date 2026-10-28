# EzTest P6 Execution & Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付「已批准基线 → 建批 → 真实浏览器/API 执行 → 暂停/恢复/检查点 → 步骤级证据 → 四态与失败分类 → Batch 1 报告 → 真实人工验收」的 P6 垂直切片。

**Architecture:** 新增 `backend/internal/execution` 限界上下文（DDD 四层，6 张表）承载确定性引擎，每条用例由执行 Agent（P1 运行时）驱动、以结构化提交写回并由服务端校验；新增独立 `executor/` Node/Playwright 长驻服务负责浏览器 context/动作/产物，Go 只通过 `BrowserExecutor` 接口编排；`resource` 补 `auth_flow` 自动登录与注入，`version` 补 paused/batch_no/完成流转，`mindmap` 补执行快照与 `executed` 标记；前端新增 `features/execution/` 五 Tab 与顶栏暂停/接管 UI。

**Tech Stack:** Go 1.24.6（net/http、pgx/v5）、PostgreSQL 17.6、Node 24 + Playwright 1.64.1、Next.js 16 / React 19 / TypeScript / Tailwind / Radix、Vitest、Playwright E2E、node:test。

**Spec:** [docs/superpowers/specs/2026-10-28-eztest-p6-execution-design.md](../specs/2026-10-28-eztest-p6-execution-design.md)

## Global Constraints

- 范围严格按 spec；**不实现** AI 补全、失败分析 Agent、问题/issue、完整报告（revision/stale/差异/持久化）、继续中/增量批次、视觉断言、DB 断言（归 P7）、Mock/夹具工具、多版本并行、跨用例语义依赖、ProbeRecipe、MCP。
- **D29**：新增表主键雪花 ID（应用层生成，DDL 无序列）；JSON/URL/事件/工具参数中的执行 ID 一律十进制字符串；前端禁止 `Number()`。
- **D30**：证据走文件系统（`EZTEST_EVIDENCE_DIR=.data/evidence`）；DB 只存路径+sha256+大小；根内校验、原子 rename、0600/0700；禁止二进制/base64 入库；读取校验哈希；无删除 API。
- **D34**：表名单数小写下划线；索引 `pk_/uk_/idx_`；新表 `created_at/updated_at`（evidence 的 `updated_at` 不更新）；迁移只追加，已应用迁移不改；`order.txt` 只由 Lead 追加。
- **D28/D41**：Session/凭据明文；`auth_flow` 最小 form；自动登录仅 password + `login_check`；失效同一用例重取一次；MFA/验证码/审批走人工接管（表单 A）；事件/日志/证据/前端不出现密码或凭据。
- **D31/D35/D37**：基线只读；执行只读基线；建批固定 `baseline_hash` + commit；`case_node reviewed→executed` 只改状态不改内容；版本 `executing→completed` 仅由批次完成触发。
- **P1 契约**：`agent.activity` 九 kind 语义不变；工具/事件只能加法扩展；工具调用审计不含参数/结果。
- **执行器**：Node/Playwright 1.64.x；仅 loopback + `X-Executor-Token`；每用例一 context；`allowed_hosts` 路由层强制；动作级超时/取消；不接业务 DB。
- **判定**：应用缺陷只来自有证据的断言失败；环境/会话 → blocked；模型/用例/预算/db 断言 → inconclusive；`skipped/interrupted` 为过程态。
- **验证**：每个任务先写失败测试 → 跑失败 → 最小实现 → 跑通过 → 提交；提交前缀 `feat/fix/test/docs/chore/refactor`。
- 分支 `feat/p6-execution`；**并行实现者不 commit**，Lead 按每个任务的写作用域 stage/commit；验收后本地 ff 合回 `dev`，不 push。
- 不提交 `.env`、`.tools/`、`.tmp/`、`.data/`、`.superpowers/sdd/`、`*.local.md`。
- 端口：Web `6868`、API `13333`、PostgreSQL `5433`、executor `13336`、fake LLM `13334`、fixture `13335`。
- 真实 LLM 验收 gated `EZTEST_RUN_REAL_LLM=1`；真实人工验收由 Lead 执行并留档。

### 命令约定

- Go 单元：`cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/...`
- 领域快测：`cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain ./internal/execution/application`
- 集成：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/execution/integration/...`
- 执行器：`cd executor; pnpm install; pnpm test`
- 前端：`cd frontend; pnpm vitest run src/features/execution`、`pnpm lint`、`pnpm typecheck`
- 全量门禁：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1`
- E2E：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 execution`
- 总验收：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1`
- 真实 LLM：`$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-execution-llm.ps1`

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | Tasks | 并行规则 |
|---|---|---|
| **W0** | T0 | Pre-flight（Lead，一次性） |
| **W1** | T1 ∥ T2 ∥ T3 ∥ T4 ∥ T5 ∥ T6 | 六个互斥写作用域：execution 领域+迁移 / version / resource / mindmap / agent / executor |
| **W2** | T7 ∥ T8 ∥ T9 ∥ T10 ∥ T11 | 执行器客户端 / 仓储 / 领域规则 / resource 自动登录 / 前端数据层；写包互不重叠 |
| **W3** | T12 ∥ T13 ∥ T14 | 批次服务 / 证据服务 / 报告服务；application 内按文件拆分，端口由 T12 定义 |
| **W4** | T15 ∥ T16 ∥ T17 ∥ T18 | 调度+用例执行 / Agent 工具+profile / 跨上下文适配器 / 恢复 worker；Recorder 接口与输入类型由 T12 定义，四者写文件互斥 |
| **W5** | T19 | HTTP API + DTO + 路由 + 组装根（依赖 T12–T18） |
| **W6** | T20 ∥ T21 ∥ T22 ∥ T23 ∥ T24 ∥ T25 ∥ T26 ∥ T27 | 顶栏暂停 / 会话 Tab / 结果 Tab / 播报 Tab / 链路图 / 报告 Tab / 登录配方 UI / fixtures+fake LLM；前端组件文件互斥 |
| **W7** | T28 ∥ T29 ∥ T30 | E2E / 验收脚本 / OpenAPI；均依赖 T19 与 T27 |
| **W8** | T31 | 文档同步 D39–D42 + 全量门禁（需全部功能稳定） |
| **W9** | T32 | 真实 LLM 验收 + 真实人工验收 + 合回 `dev`（Lead，串行） |

- 同一 Wave 内任务派发给独立 subagent；每个任务的 `**写作用域**` 是 subagent 唯一允许修改的路径前缀。
- 任务编号 `T1–T32` 只作唯一标识；执行顺序以 Wave 与依赖为准。
- **同包串行**：`execution/application` 内 `batch(T12)→evidence(T13)/report(T14)→scheduler/recorder(T15)`；T16/T18 只消费 T12 的接口与 T15 的实现，可与 T15 并行（新增方法仍只允许 T12 追加）；`execution/interfaces/httpapi` 仅 T19；`cmd/eztest`+`server` 仅 T19；前端 `features/execution` 链 T11→T20/T21/T22→T23/T24/T25。
- `db/migrations/order.txt` 只由 Lead 在 T1 提交；任何 subagent 不得修改。
- 跨 Wave 依赖未完成不得启动；并行实现者不 commit；Lead 逐任务 stage/commit 后进入下一批。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **暂停/重启竞态**：暂停请求与"用例刚好完成/步骤刚好落库"并发 → 不得丢步骤、不得重复执行、不得把优雅暂停误标 interrupted；崩溃重启必须 released 租约且不自动拉起浏览器。
2. **证据/凭据泄漏**：HTTP 证据头/体、`agent.tool_use` 参数预览、console/network、storageState、错误消息 → 任何路径都不得出现密码/Authorization/Cookie/Set-Cookie。
3. **基线漂移**：冻结后脑图行被 SQL 直接改动（应用层只读挡不住）→ 建批必须发现 `baseline_hash` 不一致并 409，不得执行被改过的用例。
4. **会话/租约争用**：自动登录单飞失败、人工接管等待期间占租约、同一 Session 并发两用例 → 不得并发注入同一会话或永久占用租约。
5. **分类误判**：环境/会话/模型错误被报成应用缺陷；预算耗尽被判 failed；db 断言假通过 → 必须 blocked/inconclusive 并说明原因。

---

## Pre-flight（T0，一次性，不算任务）

- [ ] 确认在 `dev` 且工作区干净：`git status --short`；确认 P6 spec 提交 `bac6c12` 在历史中。
- [ ] 创建分支：`git switch -c feat/p6-execution`。
- [ ] 启动数据库：`powershell -File scripts/db.ps1 status`，必要时 `start`；确认 `5433` 可用。
- [ ] 确认端口空闲：`13333/13334/13335/13336/6868`。
- [ ] 跑基线门禁：`scripts/test.ps1` 全绿；记录基线耗时。
- [ ] 确认 `frontend/node_modules` 存在、Playwright Chromium 已装（`%USERPROFILE%\AppData\Local\ms-playwright`）。
- [ ] 记录真实 LLM `llm_*` 已配置（不打印值）。

## Shared Interfaces（由 T1/T12 定义；后续任务消费，不得改名）

```go
// execution/domain
type BatchStatus string // planned|running|done|failed|cancelled
type CaseRunStatus string // pending|running|passed|failed|blocked|inconclusive|skipped|interrupted
type FailureClass string // application|environment|session|model|case
type Verdict string // passed|failed|blocked|inconclusive
type StepStatus string // pending|running|passed|failed|interrupted
type PauseMode string // graceful|immediate

type CaseSnapshot struct {
    Node       CaseSnapshotNode
    Steps      []CaseSnapshotStep
    Assertions []CaseSnapshotAssertion
    Sources    []CaseSnapshotSource
    GraphHash  string
    CapturedAt time.Time
}
type RunBatch struct {
    ID, VersionID int64; BatchNo int; Status BatchStatus; Paused bool
    PauseMode PauseMode; PauseReason string; BaselineHash string
    CodeBaselineCommit, HeadCommit *string
    CaseCount, PassedCount, FailedCount, BlockedCount, InconclusiveCount, SkippedCount, InterruptedCount int
    CreatedBy int64; StartedAt, FinishedAt *time.Time; FailureReason string
    CreatedAt, UpdatedAt time.Time
}
type CaseRun struct {
    ID, BatchID, VersionID, CaseNodeID int64; CaseRevision int; CaseHash string
    Snapshot CaseSnapshot; Attempt int; Status CaseRunStatus
    FailureClass *FailureClass; FailureReason, Summary string
    SessionID, SessionSnapshotID, AgentSessionID, AgentRunID, RetryOfCaseRunID *int64
    CleanupRequired bool; InterruptedStepSeq *int
    StartedAt, FinishedAt *time.Time; DurationMS int64; CreatedAt, UpdatedAt time.Time
}
type StepResult struct { ID, BatchID, CaseRunID int64; Seq int; Action, Expected, Actual string
    Status StepStatus; EvidenceCount int; StartedAt, FinishedAt *time.Time; CreatedAt, UpdatedAt time.Time }
type AssertionResult struct { ID, BatchID, CaseRunID int64; StepResultID *int64; AssertionID int64
    Kind, Target, Expected, Actual, Verdict, FailureReason string; CreatedAt, UpdatedAt time.Time }
type Evidence struct { ID, BatchID int64; CaseRunID, StepResultID *int64; Kind, Path, SHA256, Mime string
    SizeBytes int64; Metadata map[string]any; CreatedAt time.Time }

func CanonicalCaseHash(s CaseSnapshot) string // lowercase hex sha256 over canonical JSON

type FinishDecision struct { Verdict Verdict; FailureClass *FailureClass; Reason string }

// execution/application
type BatchService struct{ /* ... */ }
func (s *BatchService) Create(ctx context.Context, versionID, actor int64, caseIDs []int64) (*domain.RunBatch, error)
func (s *BatchService) Get(ctx context.Context, versionID, batchID int64) (*domain.RunBatch, error)
func (s *BatchService) List(ctx context.Context, versionID int64, beforeSeq int64, limit int) ([]domain.RunBatch, bool, error)
func (s *BatchService) Pause(ctx context.Context, versionID, batchID int64, mode domain.PauseMode, actor int64) (*domain.RunBatch, error)
func (s *BatchService) Resume(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
func (s *BatchService) Cancel(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
func (s *BatchService) Rerun(ctx context.Context, versionID, batchID, caseRunID, actor int64) (*domain.CaseRun, error)

type VersionPort interface {
    GetVersion(ctx context.Context, versionID int64) (VersionRef, error)
    MarkPaused(ctx context.Context, versionID int64, paused bool, mode, reason string, actor int64) error
    SetBatchNo(ctx context.Context, versionID int64, batchNo int, actor int64) error
    CompleteExecution(ctx context.Context, versionID, actor int64) error
}
type MindmapPort interface {
    Baseline(ctx context.Context, versionID int64) (BaselineRef, error)
    SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]CaseSnapshotSourceView, error)
    MarkExecuted(ctx context.Context, versionID int64, caseNodeIDs []int64) error
}
type SessionPort interface {
    Acquire(ctx context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (SessionRef, error)
    Release(ctx context.Context, sessionID int64, holder, reason string) error
    MarkInvalid(ctx context.Context, sessionID int64, reason string) error
    PendingTakeover(ctx context.Context, sessionID int64) (*TakeoverRef, error)
    Environment(ctx context.Context, environmentID int64) (EnvironmentRef, error)
}
type AgentRuntimePort interface {
    EnsureProfile(ctx context.Context) error
    StartCaseRun(ctx context.Context, req CaseRunRequest) (AgentRunRef, error)
    GetRun(ctx context.Context, agentSessionID int64) (AgentRunState, error)
    CancelRun(ctx context.Context, agentSessionID int64) error
    ListSessions(ctx context.Context, versionID int64) ([]AgentSessionRef, error)
}
type EventPublisher interface { Publish(ctx context.Context, ev Event) error }
type EvidenceStore interface {
    Write(ctx context.Context, rel string, data []byte) (string, string, int64, error)
    Open(ctx context.Context, rel string) (io.ReadCloser, error)
}
type BrowserExecutor interface {
    Health(ctx context.Context) error
    NewContext(ctx context.Context, spec ContextSpec) (ContextRef, error)
    Action(ctx context.Context, ref ContextRef, action Action) (ActionResult, error)
    Cancel(ctx context.Context, ref ContextRef) error
    Close(ctx context.Context, ref ContextRef) error
}
type StepRecordInput struct { Seq int; Status domain.StepStatus; Actual string; EvidenceIDs []int64 }
type AssertionRecordInput struct { AssertionID int64; Verdict string; Actual string; EvidenceIDs []int64 }
type FinishInput struct { Decision domain.FinishDecision; EvidenceIDs []int64 }
type CaseContextView struct { CaseRun domain.CaseRun; CompletedSeqs []int; InterruptedStepSeq *int
    CleanupRequired bool; BaseURL string; AllowedHosts []string }
type Recorder interface {
    CaseContext(ctx context.Context, caseRunID int64) (CaseContextView, error)
    RecordStep(ctx context.Context, caseRunID int64, in StepRecordInput) (domain.StepResult, error)
    RecordAssertion(ctx context.Context, caseRunID int64, in AssertionRecordInput) (domain.AssertionResult, error)
    FinishCase(ctx context.Context, caseRunID int64, in FinishInput) (domain.CaseRun, error)
}
```

---

## Wave 1 — 地基（T1–T6 可并行）

### Task 1: execution 领域类型 + 6 表迁移

**Wave 1 · 可并发：是（与 T2–T6）**

**Files:**
- Create: `backend/internal/db/migrations/execution_0001_batches.sql`
- Modify: `backend/internal/db/migrations/order.txt`
- Create: `backend/internal/execution/domain/types.go`
- Create: `backend/internal/execution/domain/entities.go`
- Create: `backend/internal/execution/domain/snapshot.go`
- Create: `backend/internal/execution/domain/errors.go`
- Test: `backend/internal/execution/domain/snapshot_test.go`

**Interfaces:**
- Produces: `BatchStatus/CaseRunStatus/FailureClass/Verdict/StepStatus/PauseMode` 常量；`CaseSnapshot` 及子结构；`CanonicalCaseHash`；实体结构体；`ReasonError{Code,Reason,Message,Retryable,Details}` 与 `NewReasonError`。
- 注：`order.txt` 的追加由 Lead 在本任务提交时执行；subagent 的写作用域**不含** `order.txt`。

- [ ] **Step 1: 写失败测试（快照哈希确定性 + 校验）**

```go
func TestCanonicalCaseHashDeterministicAndOrderSensitive(t *testing.T) {
    a := domain.CaseSnapshot{GraphHash: strings.Repeat("a", 64),
        Node: domain.CaseSnapshotNode{ID: 7, Title: "下单"},
        Steps: []domain.CaseSnapshotStep{{Seq: 1, Action: "打开列表"}},
        Assertions: []domain.CaseSnapshotAssertion{{ID: 9, Kind: "ui", Expected: "存在"}}}
    b := a
    if got, want := domain.CanonicalCaseHash(b), domain.CanonicalCaseHash(a); got != want {
        t.Fatalf("hash not deterministic: %s != %s", got, want)
    }
    b.Steps[0].Action = "打开详情"
    if domain.CanonicalCaseHash(b) == domain.CanonicalCaseHash(a) {
        t.Fatal("hash must change when a step changes")
    }
    if len(domain.CanonicalCaseHash(a)) != 64 { t.Fatal("hash must be 64 hex chars") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain`
Expected: FAIL（`undefined: domain.CaseSnapshot`）。

- [ ] **Step 3: 实现类型/实体/错误 + 迁移**

`types.go` 定义常量（值严格按 spec §3.2/§5.8）；`entities.go` 定义 `RunBatch/CaseRun/StepResult/AssertionResult/Evidence/EvidenceRef`；`snapshot.go` 用 `encoding/json` 对字段顺序固定的 struct 序列化后 `sha256.Sum256`，返回小写 hex；`errors.go` 定义 `ReasonError` 与 sentinel（`ErrBatchActive`、`ErrNotPaused` 等），Error() 返回 code+message。
迁移 DDL 逐字复制 spec §3.2（6 表 + 索引 + CHECK），并在 `order.txt` 末尾追加 `execution_0001_batches.sql`。
测试补充 `TestReasonErrorCarriesReason`（reason 原样保留）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain`
Expected: PASS。

- [ ] **Step 5: 迁移存在性自检**

Run: `cd backend; ..\.tools\go\bin\go.exe build ./...`
Expected: 成功（不连库）。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/db/migrations/execution_0001_batches.sql backend/internal/db/migrations/order.txt backend/internal/execution/domain
git commit -m "feat: add P6 execution schema and domain types"
```

### Task 2: version 执行状态（paused / batch_no / completed）

**Wave 1 · 可并发：是（与 T1、T3–T6）**

**Files:**
- Create: `backend/internal/db/migrations/version_0004_execution_state.sql`
- Modify: `backend/internal/version/domain/version.go`
- Modify: `backend/internal/version/domain/repository.go`
- Modify: `backend/internal/version/application/version_service.go`
- Modify: `backend/internal/version/infrastructure/postgres/repository.go`
- Test: `backend/internal/version/application/execution_state_test.go`（用既有 fake repo 风格）
- Test: `backend/internal/version/infrastructure/postgres/repository_test.go`（补列扫描）

**Interfaces:**
- Produces: `Version.Paused bool`、`Version.PauseMode string`、`Version.BatchNo int`；`VersionService.MarkPaused(ctx,id int64,paused bool,mode,reason string,actor int64) (*Version,error)`；`SetBatchNo(ctx,id int64,batchNo int,actor int64) (*Version,error)`；`CompleteExecution(ctx,id,actor int64) (*Version,error)`。

- [ ] **Step 1: 写失败测试**

```go
func TestMarkPausedRequiresExecutingAndMirrorsState(t *testing.T) {
    svc, repo := newVersionServiceForTest(t) // 既有测试夹具
    v := repo.seedVersion(domain.VersionExecuting)
    paused, err := svc.MarkPaused(ctx, v.ID, true, "graceful", "user", 1)
    if err != nil || !paused.Paused || paused.Status != domain.VersionExecuting {
        t.Fatalf("pause must not change status: %+v %v", paused, err)
    }
    if _, err := svc.CompleteExecution(ctx, v.ID, 1); err != nil { t.Fatal(err) }
    if got, _ := repo.Get(ctx, v.ID); got.Status != domain.VersionCompleted { t.Fatalf("got %s", got.Status) }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/application -run TestMarkPaused`
Expected: FAIL（方法不存在）。

- [ ] **Step 3: 实现**

迁移逐字复制 spec §3.3；domain 增字段 + `PauseModeGraceful/Immediate` 校验；repo 增 `UpdateExecutionState(ctx,id,paused,pauseMode,batchNo,actor)` 与 `UpdateStatus` 复用；service 中：`MarkPaused` 仅允许 `executing|continuing`，不改 status，发 `version.status_changed` 语义事件（detail 含 paused）；`SetBatchNo` 单调递增校验；`CompleteExecution` 仅 `executing→completed`，否则 `version_state_conflict`，发 `version.status_changed`。
补充测试：非 executing 暂停 409；batch_no 回退拒绝；重复 complete 冲突。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/version/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/migrations/version_0004_execution_state.sql backend/internal/version
git commit -m "feat: add version execution pause and batch state"
```

### Task 3: resource auth_flow + sessionStorage 存储态

**Wave 1 · 可并发：是（与 T1、T2、T4–T6）**

**Files:**
- Create: `backend/internal/db/migrations/resource_0006_auth_flows.sql`
- Create: `backend/internal/resource/domain/auth_flow.go`
- Modify: `backend/internal/resource/domain/repository.go`
- Create: `backend/internal/resource/infrastructure/postgres/auth_flow_repository.go`
- Create: `backend/internal/resource/application/auth_flow_service.go`
- Modify: `backend/internal/resource/interfaces/httpapi/routes.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_auth_flow.go`
- Modify: `backend/internal/resource/domain/storage_state.go`
- Test: `backend/internal/resource/domain/auth_flow_test.go`
- Test: `backend/internal/resource/domain/storage_state_session_test.go`
- Test: `backend/internal/resource/interfaces/httpapi/handlers_auth_flow_test.go`

**Interfaces:**
- Produces: `domain.AuthFlow{ID,SystemID int64; Name,Kind string; Definition FormDefinition; Enabled bool}`；`domain.FormDefinition{LoginURL,UsernameSelector,PasswordSelector,SubmitSelector string; Success SuccessRule; Failure FailureSelectors; TimeoutMS int}`；`AuthFlowService.Create/Get/List/Update/Delete`；存储态扩展保留 `sessionStorage`。

- [ ] **Step 1: 写失败测试**

```go
func TestFormDefinitionValidatesRequiredSelectors(t *testing.T) {
    d := domain.FormDefinition{LoginURL: "https://x.test/login", UsernameSelector: "#u"}
    if err := d.Validate(); err == nil { t.Fatal("missing password/submit/success must fail") }
}
func TestParseStorageStateKeepsSessionStorage(t *testing.T) {
    raw := []byte(`{"cookies":[],"origins":[{"origin":"https://x.test","localStorage":[{"name":"a","value":"1"}],"sessionStorage":[{"name":"b","value":"2"}]}]}`)
    st, err := domain.ParseStorageState("storage_state", raw)
    if err != nil || len(st.Origins) != 1 || len(st.Origins[0].SessionStorage) != 1 { t.Fatalf("%+v %v", st, err) }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/domain -run 'AuthFlow|SessionStorage'`
Expected: FAIL。

- [ ] **Step 3: 实现**

迁移逐字复制 spec §3.4（auth_flow + access.login_flow_id）。`FormDefinition.Validate`：URL 必须 http(s)、选择器非空、success 二选一、timeout 5000–60000。仓储用雪花 ID（依赖 `platform/id`）；service 只 allow admin（HTTP 层 guard）。HTTP：`GET/POST /api/resources/auth-flows`、`GET/PATCH/DELETE /api/resources/auth-flows/{id}`，DTO 字符串 ID，错误 reason `auth_flow_invalid`。`storage_state.go` 的 Origin 结构增 `SessionStorage []LocalStorageEntry`，normalize/round-trip 保留；旧数据不变。
补测试：非法 URL/超时拒绝、CRUD 分页、普通用户 403、sessionStorage 往返。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/migrations/resource_0006_auth_flows.sql backend/internal/resource
git commit -m "feat: add auth flow recipes and sessionStorage state"
```

### Task 4: mindmap 执行快照与 MarkExecuted

**Wave 1 · 可并发：是（与 T1–T3、T5、T6）**

**Files:**
- Create: `backend/internal/mindmap/domain/execution.go`
- Create: `backend/internal/mindmap/application/execution_service.go`
- Test: `backend/internal/mindmap/application/execution_service_test.go`

**Interfaces:**
- Produces: `ExecutionBaseline{GraphHash,CodeBaselineCommit string; CaseIDs []int64}`；`ExecutionCase{NodeID,Revision int64; Title,Priority,Preconditions string; TestData json.RawMessage; Steps []ExecutionStep; Assertions []ExecutionAssertion; RequirementIDs []int64; GraphHash string}`；`ExecutionService.Baseline/SnapshotCases/MarkExecuted`。

- [ ] **Step 1: 写失败测试**

```go
func TestMarkExecutedOnlyReviewedCasesAndKeepsBaseline(t *testing.T) {
    svc, repos := newExecutionServiceTest(t) // 既有 fake graph/repo 夹具
    repos.seedReviewedCase(42)
    if err := svc.MarkExecuted(ctx, 7, []int64{42}); err != nil { t.Fatal(err) }
    if got := repos.nodeStatus(42); got != domain.CaseStatusExecuted { t.Fatalf("got %s", got) }
    if repos.baselineWrites != 0 { t.Fatal("MarkExecuted must not touch baseline") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/mindmap/application -run TestMarkExecuted`
Expected: FAIL。

- [ ] **Step 3: 实现**

`Baseline` 读 `case_baseline`（hash/commit），返回全部 `kind=case` 节点 id；`SnapshotCases` 复用 `GraphService.Get` 的读模型，按 `caseIDs` 过滤并组装 `ExecutionCase`（步骤按 seq、断言按 id）；`MarkExecuted` 只允许 `reviewed→executed`，幂等（已 executed 不报错），不改内容/基线/事件以外的行；发 `case.status_changed`（复用既有 publish）。
补测试：未 reviewed 拒绝；空 caseIDs=全部；快照步骤/断言顺序；已 executed 幂等。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/mindmap/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/mindmap/domain/execution.go backend/internal/mindmap/application/execution_service.go backend/internal/mindmap/application/execution_service_test.go
git commit -m "feat: expose mindmap execution snapshots and executed marker"
```

### Task 5: P1 加法扩展（Invocation / 工具超时 / 会话查询）

**Wave 1 · 可并发：是（与 T1–T4、T6）**

**Files:**
- Modify: `backend/internal/agent/application/tool_invocation.go`
- Modify: `backend/internal/agent/domain/tool.go`
- Modify: `backend/internal/agent/application/tool_executor.go`
- Modify: `backend/internal/agent/domain/repository.go`
- Modify: `backend/internal/agent/infrastructure/postgres/session_repository.go`
- Modify: `backend/internal/agent/application/session_service.go`
- Modify: `backend/internal/agent/interfaces/httpapi/handlers.go`
- Modify: `backend/internal/agent/interfaces/httpapi/dto.go`
- Test: `backend/internal/agent/application/tool_executor_timeout_test.go`
- Test: `backend/internal/agent/application/session_list_test.go`

**Interfaces:**
- Produces: `Invocation.BatchID/CaseRunID int64`；`domain.ToolWithTimeout interface{ Timeout() time.Duration }`；`SessionService.ListByChannel(ctx, channel string, beforeSeq int64, limit int) ([]domain.Session, bool, error)`；`GET /api/agents/sessions?channel=&cursor=&limit=`。

- [ ] **Step 1: 写失败测试**

```go
func TestToolTimeoutOverridesGlobal(t *testing.T) {
    slow := &timeoutTool{d: 2 * time.Second} // 实现 ToolWithTimeout
    ex := newExecutorForTest(50 * time.Millisecond)
    res := ex.Execute(ctx, "slow", nil, Invocation{})
    if res.Status != "timeout" { t.Fatalf("want timeout, got %s", res.Status) }
}
func TestListSessionsByActivityChannel(t *testing.T) {
    svc, repo := newSessionServiceForTest(t)
    repo.seedChannel("version:9", 2)
    got, more, err := svc.ListByChannel(ctx, "version:9", 0, 10)
    if err != nil || len(got) != 2 || more { t.Fatalf("%v %v", got, err) }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/application -run 'ToolTimeout|ListSessionsByActivity'`
Expected: FAIL。

- [ ] **Step 3: 实现**

`Invocation` 增两字段（默认 0）；`ToolWithTimeout` 在 executor 中优先取工具值（上限 60s，0 则用全局）；不改变既有 9 个 kind 与审计字段。Session repository 增 `ListByChannel`（`agent_session.activity_channel=?`，按 updated_at/id 游标）；service 包装并校验 channel 非空；handler `GET /api/agents/sessions` 仅 admin（沿用 P1 guard），DTO 含 id/channel/title/status/running。补测试：无超时接口仍用全局；invocation 透传到工具 ctx。
**Review Focus #2**：本任务保证 `Invocation` 不携带任何凭据/提示词。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat: extend agent runtime for execution runs"
```

### Task 6: Node/Playwright 执行器服务

**Wave 1 · 可并发：是（与 T1–T5）**

**Files:**
- Create: `executor/package.json`, `executor/pnpm-lock.yaml`, `executor/src/{server.mjs,contexts.mjs,actions.mjs,snapshot.mjs,security.mjs}`
- Create: `executor/test/{server.test.mjs,actions.test.mjs,snapshot.test.mjs,security.test.mjs}`
- Create: `executor/Dockerfile`, `executor/README.md`
- Modify: `deploy/docker-compose.yml`（新增 `executor` 服务、shm、健康检查、API 注入 `EZTEST_EXECUTOR_URL`）
- Create: `scripts/dev-executor.ps1`
- Modify: `scripts/dev-api.ps1`（执行器健康提示）
- Modify: `.gitignore`（如需忽略 executor 临时下载目录）

**Interfaces:**
- Produces: HTTP 协议与动作（spec §5.6/§8）：`GET /healthz`、`POST /v1/contexts`、`POST /v1/contexts/{id}/actions`、`POST /v1/contexts/{id}/cancel`、`DELETE /v1/contexts/{id}`；错误码 `invalid_request|unauthorized|host_not_allowed|navigation_failed|dns_failed|connection_failed|timeout|target_not_found|target_ambiguous|canceled|context_not_found|context_limit|too_large|internal`。

- [ ] **Step 1: 写失败测试（node:test + 本地 http fixture）**

```js
test('rejects non-allowed host before navigation', async () => {
  const ctx = await createContext({ allowedHosts: ['127.0.0.1'], baseUrl: 'http://127.0.0.1:1' })
  const r = await runAction(ctx, { action: 'goto', args: { url: 'http://example.com/' } })
  assert.equal(r.ok, false); assert.equal(r.error.code, 'host_not_allowed')
})
test('screenshot returns png artifact with sha256', async () => {
  const pageServer = await startLocalPage() // 本地 http.createServer 返回 /home
  const ctx = await createContext({ allowedHosts: ['127.0.0.1'], baseUrl: pageServer })
  await runAction(ctx, { action: 'goto', args: { url: pageServer + '/home' } })
  const r = await runAction(ctx, { action: 'screenshot', args: { full_page: true } })
  assert.ok(r.ok); assert.equal(r.artifacts[0].mime, 'image/png')
  assert.match(r.artifacts[0].sha256, /^[0-9a-f]{64}$/)
})
```

- [ ] **Step 2: 跑失败**

Run: `cd executor; pnpm install; pnpm test`
Expected: FAIL（模块不存在）。

- [ ] **Step 3: 实现**

`server.mjs`：Node http、Bearer token、1MB body 上限、路由与错误信封；`contexts.mjs`：单 Browser、context 上限、per-context action 串行队列、AbortController、close 清理；`actions.mjs`：goto/snapshot/click/fill/press/wait_for/screenshot/console/network/download/storage_state，target 解析与错误映射；`snapshot.mjs`：`page.locator('body').ariaSnapshot()` + 12KB 截断 + 密码值剥离；`security.mjs`：`isAllowedHost`（精确/`*.suffix`）、route 拦截、artifact 大小上限。
`package.json` 钉 `playwright@1.64.1`，scripts `start`/`test`；Dockerfile 基于 `mcr.microsoft.com/playwright:v1.64.1-noble`；`scripts/dev-executor.ps1` 读取 `.env` 的 token 并启动。
补测试：token 401；target 0/多匹配；cancel 终止慢动作；download；console/network 环形；storage_state 含 sessionStorage。

- [ ] **Step 4: 跑通过**

Run: `cd executor; pnpm test`
Expected: 全绿。

- [ ] **Step 5: 手工冒烟**

Run: `$env:EZTEST_EXECUTOR_TOKEN="dev"; cd executor; pnpm start`（另一终端）`curl http://127.0.0.1:13336/healthz`
Expected: `{"status":"ok",...}`。

- [ ] **Step 6: 提交**

```bash
git add executor scripts/dev-executor.ps1
git commit -m "feat: add node playwright executor service"
```

---

## Wave 2 — 客户端 / 仓储 / 规则 / 自动登录 / 前端数据层（T7–T11 可并行）

### Task 7: Go 执行器客户端 + fake executor

**Wave 2 · 可并发：是（与 T8–T11）**

**Files:**
- Create: `backend/internal/execution/application/browser_port.go`
- Create: `backend/internal/execution/infrastructure/browser/client.go`
- Create: `backend/internal/execution/infrastructure/browser/client_test.go`
- Create: `backend/internal/execution/testutil/executor.go`

**Interfaces:**
- Produces: `BrowserExecutor`、`ContextSpec{Key string; AllowedHosts []string; BaseURL string; StorageState json.RawMessage; SessionStorage map[string]json.RawMessage; Viewport struct{Width,Height int}}`、`ContextRef{ID string}`、`Action{Name string; Args json.RawMessage}`、`ActionResult{Result json.RawMessage; Artifacts []Artifact; ErrorCode,ErrorMessage string}`、`Artifact{Kind,Mime,Filename string; Data []byte; SHA256 string}`。

- [ ] **Step 1: 写失败测试（httptest 假执行器）**

```go
func TestClientActionSurfacesTypedError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "Bearer tok" { w.WriteHeader(401); return }
        _, _ = w.Write([]byte(`{"ok":false,"error":{"code":"target_not_found","message":"none"}}`))
    }))
    defer srv.Close()
    c := browser.New(srv.URL, "tok", time.Second)
    ref, _ := c.NewContext(ctx, browser.ContextSpec{Key: "case_run:1"})
    res, err := c.Action(ctx, ref, browser.Action{Name: "click"})
    if err != nil || res.ErrorCode != "target_not_found" { t.Fatalf("%+v %v", res, err) }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/browser`
Expected: FAIL。

- [ ] **Step 3: 实现**

stdlib `net/http` + `encoding/json`；超时按调用传入；非 2xx 映射为 `internal`/`unauthorized`；artifact base64 解码并重算 sha256（与返回不一致 → `internal`）；`Health` 解析版本/容量。`testutil/executor.go` 提供内存 FakeBrowserExecutor（脚本化动作、注入错误、记录调用）供后续测试。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/browser`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/browser_port.go backend/internal/execution/infrastructure/browser backend/internal/execution/testutil
git commit -m "feat: add go browser executor client"
```

### Task 8: execution PostgreSQL 仓储

**Wave 2 · 可并发：是（与 T7、T9–T11）**

**Files:**
- Create: `backend/internal/execution/application/repository.go`（仓储接口）
- Create: `backend/internal/execution/infrastructure/postgres/{batch_repository.go,case_run_repository.go,step_repository.go,assertion_repository.go,evidence_repository.go,evidence_ref_repository.go}`
- Test: `backend/internal/execution/integration/repository_test.go`（`//go:build integration`）
- Test: `backend/internal/execution/infrastructure/postgres/scan_test.go`（无库单测：扫描/参数映射）

**Interfaces:**
- Produces: `BatchRepository{CreateBatchWithRuns, GetBatch, ListBatches, UpdateBatchStatus, SetPaused, UpdateCounts, UpdateBatchFailure}`；`CaseRunRepository{CreateCaseRun, GetCaseRun, GetCaseRunForUpdate, ListCaseRuns, NextPending, UpdateCaseRun, SetStatus, FinishCaseRun}`；`StepRepository/Upsert`；`AssertionRepository/Upsert`；`EvidenceRepository{Create,Get,ListByCaseRun}`；`EvidenceRefRepository{Create}`。

- [ ] **Step 1: 写失败测试**

```go
func TestOnlyOneActiveBatchPerVersion(t *testing.T) {
    db := testutil.OpenTestDB(t) // 既有 helper
    repo := postgres.NewBatchRepository(db.Pool)
    _, err := repo.CreateBatchWithRuns(ctx, batch(1, "running"), nil)
    if err != nil { t.Fatal(err) }
    _, err = repo.CreateBatchWithRuns(ctx, batch(1, "running"), nil)
    if err == nil { t.Fatal("second active batch must violate uk_run_batch_active") }
}
func TestNextPendingOrdersByPriorityThenID(t *testing.T) { /* seed P2/P0；断言先 P0 */ }
```

- [ ] **Step 2: 跑失败**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/execution/integration/...`
Expected: FAIL（包/表不存在）。

- [ ] **Step 3: 实现**

接口放 `application/repository.go`；postgres 实现用 pgx、显式列、`RETURNING`、事务内计数更新；`NextPending` 用 `FOR UPDATE SKIP LOCKED` + 排序 `CASE priority WHEN 'P0' THEN 0 ... END, case_node_id`；`SetPaused`/`UpdateCounts` 用 CAS；`GetCaseRun` 一次查步骤/断言/证据（或多次查询）。扫描测试不连库，覆盖 `jsonb` 与 NULL。

- [ ] **Step 4: 跑通过**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/execution/integration/...`
Expected: PASS（含唯一索引/排序/计数）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/repository.go backend/internal/execution/infrastructure/postgres backend/internal/execution/integration
git commit -m "feat: add execution postgres repositories"
```

### Task 9: 判定 / 分类 / 暂停 / 检查点 / 聚合规则

**Wave 2 · 可并发：是（与 T7、T8、T10、T11）**

**Files:**
- Create: `backend/internal/execution/domain/verdict.go`
- Create: `backend/internal/execution/domain/classification.go`
- Create: `backend/internal/execution/domain/pause.go`
- Create: `backend/internal/execution/domain/checkpoint.go`
- Create: `backend/internal/execution/domain/report.go`
- Test: 同名 `*_test.go`（5 个）

**Interfaces:**
- Produces: `ValidateFinish(snapshot CaseSnapshot, steps []StepResult, assertions []AssertionResult, decision FinishDecision) error`；`Classify(toolCode string, proposed FailureClass, evidenceCount int) (Verdict, FailureClass, string)`；`CanPause(status BatchStatus) bool`、`PauseCaseOutcome(mode PauseMode, stepInFlight bool) (CaseRunStatus, bool)`；`Checkpoint(steps []StepResult) (completed []int, nextSeq int)`；`Aggregate(runs []CaseRun) BatchCounts`、`FlakyCaseIDs(runs []CaseRun) []int64`。

- [ ] **Step 1: 写失败测试（矩阵）**

```go
func TestClassifyNeverReturnsApplicationForEnvOrSession(t *testing.T) {
    cases := []struct{ code string; want Verdict; class FailureClass }{
        {"host_not_allowed", VerdictBlocked, FailureClassEnvironment},
        {"dns_failed", VerdictBlocked, FailureClassEnvironment},
        {"session_unauthorized", VerdictBlocked, FailureClassSession},
        {"budget_exhausted", VerdictInconclusive, FailureClassModel},
        {"unsupported_assertion_kind", VerdictInconclusive, FailureClassCase},
    }
    for _, c := range cases {
        gotVerdict, gotClass, _ := domain.Classify(c.code, "", 0)
        if gotVerdict != c.want || gotClass != c.class { t.Fatalf("%s -> %s/%s", c.code, gotVerdict, gotClass) }
    }
    if v, cl, _ := domain.Classify("", domain.FailureClassEnvironment, 0); v != VerdictBlocked || cl != domain.FailureClassEnvironment {
        t.Fatal("proposed env class must normalize to blocked")
    }
}
func TestValidateFinishRequiresEvidenceForPassed(t *testing.T) { /* passed 无证据 -> evidence_missing */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain -run 'Classify|ValidateFinish|Pause|Checkpoint|Aggregate'`
Expected: FAIL。

- [ ] **Step 3: 实现**

表驱动映射（spec §5.8）：工具码优先，agent 提议只在不冲突时采纳；`target_not_found` → failed/application（有证据时），`timeout` 按 `environment` 默认；`ValidateFinish` 校验步骤全记录、非 db 断言全提交、passed/failed 证据≥1、db 断言→inconclusive、预算→inconclusive；`PauseCaseOutcome` 实现优雅→pending（不 interrupted）/立即→interrupted；`Checkpoint` 返回连续已完成 seq 与 next；`Aggregate` 计数与 flaky 派生（同 case_node 前 failed(application) 后 passed）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain`
Expected: PASS（矩阵全覆盖）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/domain
git commit -m "feat: add execution verdict classification and checkpoint rules"
```

### Task 10: resource 自动登录 / 会话获取 / 失效标记

**Wave 2 · 可并发：是（与 T7、T8、T9、T11）**

**Files:**
- Create: `backend/internal/resource/application/acquire.go`
- Create: `backend/internal/resource/application/login_port.go`
- Modify: `backend/internal/resource/application/session_service.go`
- Modify: `backend/internal/resource/application/ports.go`
- Modify: `backend/internal/resource/domain/session.go`
- Test: `backend/internal/resource/application/acquire_test.go`

**Interfaces:**
- Produces: `SessionService.AcquireForExecution(ctx, envID, systemID int64, holder string, ttl time.Duration) (AcquiredSession, error)`；`MarkInvalid(ctx, sessionID int64, reason string) error`；`LoginBrowser` 端口；`AcquiredSession{SessionID, SnapshotID int64; StorageState json.RawMessage; SessionStorage json.RawMessage; ExpiresAt *time.Time}`；`ErrNeedsManual{TakeoverID int64; Reason string}`。

- [ ] **Step 1: 写失败测试**

```go
func TestAcquireReusesValidSessionBeforeLogin(t *testing.T) {
    svc, deps := newAcquireTest(t)
    deps.repos.seedValidSession(10, "valid")
    got, err := svc.AcquireForExecution(ctx, 1, 2, "case_run:7", time.Minute)
    if err != nil || got.SessionID != 10 { t.Fatalf("%+v %v", got, err) }
    if deps.login.calls != 0 { t.Fatal("must not login when a valid session exists") }
}
func TestAcquireMFAProducesTakeover(t *testing.T) {
    svc, deps := newAcquireTest(t)
    deps.repos.seedAccess(flowEnabled)
    deps.login.outcome = "mfa"
    _, err := svc.AcquireForExecution(ctx, 1, 2, "case_run:7", time.Minute)
    var nm *application.ErrNeedsManual
    if !errors.As(err, &nm) || nm.Reason != "mfa" { t.Fatalf("%v", err) }
    if deps.repos.takeovers != 1 { t.Fatal("must create one waiting takeover") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/application -run TestAcquire`
Expected: FAIL。

- [ ] **Step 3: 实现**

`AcquireForExecution`：筛选 valid 会话（access enabled/limit/lease 空闲，最久未用）→ `AcquireLease`；否则 eligible access（password/enabled/flow/allow_auto_login）→ 单飞 `(account,system,env)` → 创建/复用 session 行（pending→acquiring）→ `LoginBrowser.Login` → 按 outcome 落状态：valid（storageState+sessStorage、acquisition=auto、expires_at、login_check、snapshot）/needs_manual（+takeover waiting）/failed；再取会话+租约。`MarkInvalid` 走 CAS 并释放租约。`LoginBrowser` 接口只依赖 domain 类型，不导入 execution。
补测试：并发两次只登录一次；租约占满返回 `ErrNoSession`；invalid→重取一次；prod+approval→needs_manual。
**Review Focus #4**：租约在 needs_manual 时必须释放（断言 `activeLease==0`）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/resource/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/resource/application backend/internal/resource/domain/session.go
git commit -m "feat: add session acquisition and auto login"
```

### Task 11: 前端执行数据层

**Wave 2 · 可并发：是（与 T7–T10）**

**Files:**
- Create: `frontend/src/features/execution/{types.ts,api.ts,execution-errors.ts}`
- Create: `frontend/src/features/execution/hooks/useExecution.ts`
- Test: `frontend/src/features/execution/{api.test.ts,execution-errors.test.ts,useExecution.test.tsx}`

**Interfaces:**
- Produces: `Batch/CaseRun/StepResultView/AssertionView/EvidenceView/ReportView` 类型；`api.ts` 函数 `createBatch/listBatches/getBatch/pauseBatch/resumeBatch/cancelBatch/listCaseRuns/getCaseRun/rerunCase/getReport/exportReport/getEvidenceUrl/getEvidenceMeta/listExecutionSessions`；hooks `useBatches/useBatch/useCaseRuns/useCaseRun/useReport/useExecutionActions`；`executionErrorMessage(code)`。

- [ ] **Step 1: 写失败测试**

```tsx
it('maps batch_active to a readable message', () => {
  expect(executionErrorMessage('batch_active')).toContain('已有执行中的批次')
})
it('useExecutionActions.pause posts mode and refreshes', async () => { /* mock api + renderHook */ })
```

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/execution`
Expected: FAIL。

- [ ] **Step 3: 实现**

所有 ID 用 string；HTTP 只走 `api-client`；错误码映射覆盖 spec §6.4 的 19 个 reason；hooks 使用 `AbortController`/请求序号，不做乐观更新；导出报告用 `apiStreamFetch` 触发下载（文件名来自 `Content-Disposition`）。SSE 订阅复用 `useVersionStream` 并在 hook 暴露 `streamStatus`。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/execution`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/execution
git commit -m "feat: add execution frontend data layer"
```

---

## Wave 3 — 批次 / 证据 / 报告服务（T12–T14 可并行）

### Task 12: 批次服务 + 端口定义

**Wave 3 · 可并发：是（与 T13、T14）**

**Files:**
- Create: `backend/internal/execution/application/ports.go`
- Create: `backend/internal/execution/application/ids.go`
- Create: `backend/internal/execution/application/batch_service.go`
- Test: `backend/internal/execution/application/batch_service_test.go`

**Interfaces:**
- Produces: `VersionPort/MindmapPort/SessionPort/AgentRuntimePort/EventPublisher/EvidenceStore/Clock/IDGenerator`（签名见 Shared Interfaces）；`Recorder` 接口与 `StepRecordInput{Seq int; Status domain.StepStatus; Actual string; EvidenceIDs []int64}`、`AssertionRecordInput{AssertionID int64; Verdict string; Actual string; EvidenceIDs []int64}`、`FinishInput{Decision domain.FinishDecision; EvidenceIDs []int64}`、`CaseContextView{...}`；`BatchService`（签名见 Shared Interfaces）；`BaselineRef{Hash string; CodeCommit *string; CaseIDs []int64}`；`CaseSnapshotSourceView{CaseNodeID int64; Revision int; GraphHash string; Node domain.CaseSnapshotNode; Steps []domain.CaseSnapshotStep; Assertions []domain.CaseSnapshotAssertion; RequirementIDs []int64}`；`SessionRef{SessionID, SnapshotID int64; StorageState json.RawMessage; SessionStorage json.RawMessage; Headers map[string]string; ExpiresAt *time.Time}`；`Event{Type string; VersionID,BatchID,CaseID,StepID int64; AgentRole,SessionID string; Summary string; Detail map[string]any; Refs EventRefs}`。

- [ ] **Step 1: 写失败测试**

```go
func TestCreatePinsBaselineAndSnapshots(t *testing.T) {
    svc, deps := newBatchServiceTest(t)
    deps.version.status = domain.VersionExecuting
    deps.mindmap.baseline = BaselineRef{Hash: strings.Repeat("a", 64), CaseIDs: []int64{11, 12}}
    b, err := svc.Create(ctx, 7, 1, nil)
    if err != nil { t.Fatal(err) }
    if b.BatchNo != 1 || b.BaselineHash != strings.Repeat("a", 64) || b.CaseCount != 2 { t.Fatalf("%+v", b) }
    if deps.events.types[0] != "batch.created" { t.Fatalf("events: %v", deps.events.types) }
}
func TestCreateRejectsActiveBatchAndBaselineDrift(t *testing.T) { /* batch_active / baseline_changed */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run TestCreatePins`
Expected: FAIL。

- [ ] **Step 3: 实现**

`ports.go` 锁死共享端口；`Create` 顺序：version → baseline → snapshot（hash 校验）→ 事务创建 batch+runs → `SetBatchNo` → 发 `batch.created/started`；`Pause/Resume/Cancel/Rerun` 校验状态机并发布事件；`Rerun` 复制 snapshot 建 attempt+1（`retry_of_case_run_id`）。错误全部用 `domain.ReasonError`。
补测试：非 executing、无基线、空用例、超上限、并发活跃批次、暂停不改 status、取消 pending→skipped、rerun 终态校验。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/ports.go backend/internal/execution/application/ids.go backend/internal/execution/application/batch_service.go backend/internal/execution/application/batch_service_test.go
git commit -m "feat: add execution batch service"
```

### Task 13: 证据服务 + 文件存储 + 脱敏

**Wave 3 · 可并发：是（与 T12、T14）**

**Files:**
- Create: `backend/internal/execution/application/evidence_service.go`
- Create: `backend/internal/execution/infrastructure/filestore/store.go`
- Test: `backend/internal/execution/application/evidence_service_test.go`
- Test: `backend/internal/execution/infrastructure/filestore/store_test.go`

**Interfaces:**
- Produces: `EvidenceService.Capture(ctx, in CaptureInput) (domain.Evidence, error)`、`Open(ctx, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error)`、`ListByCaseRun(ctx, caseRunID int64) ([]domain.Evidence, error)`；`CaptureInput{BatchID,CaseRunID int64; StepResultID *int64; Kind, Mime string; Data []byte; Metadata map[string]any}`；脱敏函数 `RedactHTTP(headers map[string][]string, body []byte) (map[string][]string, []byte)`。

- [ ] **Step 1: 写失败测试**

```go
func TestCaptureHashesAndReadsBack(t *testing.T) {
    svc, deps := newEvidenceServiceTest(t)
    ev, err := svc.Capture(ctx, CaptureInput{BatchID: 1, CaseRunID: 2, Kind: "screenshot", Mime: "image/png", Data: []byte("png")})
    if err != nil { t.Fatal(err) }
    rc, got, err := svc.Open(ctx, 7, ev.ID)
    if err != nil { defer rc.Close(); t.Fatal(err) }
    if got.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("png"))) { t.Fatal("hash mismatch") }
}
func TestRedactHTTPStripsSecrets(t *testing.T) {
    h, b := RedactHTTP(map[string][]string{"Authorization": {"Bearer x"}, "Cookie": {"a=b"}}, []byte(`{"password":"p"}`))
    if strings.Contains(fmt.Sprint(h), "Bearer x") || strings.Contains(string(b), `"p"`) { t.Fatal("leaked") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/... -run 'Capture|Redact'`
Expected: FAIL。

- [ ] **Step 3: 实现**

`Capture` 计算 sha256、检查单条/单用例上限、写临时文件+rename（复用 mindmap filestore 的 `resolvePath` 思路，新实现避免跨上下文依赖）、DB 写 evidence+ref；`Open` 校验根内路径、哈希、版本归属；`RedactHTTP` 头打码与 JSON 键名打码（递归）。无 Update/Delete。
补测试：路径穿越拒绝、超限报错、跨版本 404、哈希不符报错。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/filestore ./internal/execution/application -run 'Evidence|Redact'`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/evidence_service.go backend/internal/execution/infrastructure/filestore
git commit -m "feat: add execution evidence storage with redaction"
```

### Task 14: Batch 1 报告服务

**Wave 3 · 可并发：是（与 T12、T13）**

**Files:**
- Create: `backend/internal/execution/application/report_service.go`
- Test: `backend/internal/execution/application/report_service_test.go`

**Interfaces:**
- Produces: `ReportService.BatchReport(ctx, versionID, batchID int64) (ReportView, error)`、`Markdown(ctx, versionID, batchID int64) ([]byte, string, error)`；`ReportView{Batch ReportBatchView; Summary ReportSummary; Cases []ReportCase; Environment map[string]string; GeneratedAt time.Time}`。

- [ ] **Step 1: 写失败测试**

```go
func TestReportAggregatesCountsAndFlaky(t *testing.T) {
    svc, deps := newReportServiceTest(t)
    deps.seedCaseRuns(domain.CaseRunStatusPassed, domain.CaseRunStatusFailed, domain.CaseRunStatusBlocked)
    deps.seedAttempt(10, 1, domain.CaseRunStatusFailed, domain.FailureClassApplication)
    deps.seedAttempt(10, 2, domain.CaseRunStatusPassed, "")
    r, err := svc.BatchReport(ctx, 7, 1)
    if err != nil { t.Fatal(err) }
    if r.Summary.ByStatus["passed"] != 2 || len(r.Summary.FlakyCaseIDs) != 1 { t.Fatalf("%+v", r.Summary) }
}
func TestMarkdownContainsBatchAndCases(t *testing.T) { /* 断言标题/计数/用例标题/commit */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run TestReport`
Expected: FAIL。

- [ ] **Step 3: 实现**

只用批次/用例/断言/证据派生，不建表；`Markdown` 确定性输出（批次、baseline/commit、四态、分类、用例表、证据计数、环境、时间），返回 sha256；不包含问题/差异/stale。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run TestReport`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/report_service.go backend/internal/execution/application/report_service_test.go
git commit -m "feat: add batch one execution report"
```

---

## Wave 4 — 调度执行 / Agent 工具 / 适配器（T15–T17）

### Task 15: 调度器 + 用例执行器 + 结构化记录

**Wave 4 · 可并发：是（与 T16–T18）**

**Files:**
- Create: `backend/internal/execution/application/scheduler.go`
- Create: `backend/internal/execution/application/case_runner.go`
- Create: `backend/internal/execution/application/recorder.go`
- Test: `backend/internal/execution/application/scheduler_test.go`
- Test: `backend/internal/execution/application/recorder_test.go`

**Interfaces:**
- Produces: `Scheduler.Start(ctx)`、`RunOnce(ctx) error`、`Notify()`；实现 T12 的 `Recorder`（方法签名见 Shared Interfaces）；`StepRecordInput/AssertionRecordInput/FinishInput` 由 T12 定义（本任务消费）。

- [ ] **Step 1: 写失败测试**

```go
func TestSchedulerRespectsConcurrencyAndPriority(t *testing.T) {
    s, deps := newSchedulerTest(t, 2)
    deps.seedPending(1, "P2"); deps.seedPending(2, "P0"); deps.seedPending(3, "P1")
    deps.blockRuns() // case runner 卡住直到 release
    _ = s.RunOnce(ctx)
    if got := deps.startedOrder(); !reflect.DeepEqual(got, []int64{2, 3}) { t.Fatalf("got %v", got) }
}
func TestGracefulPauseParksPendingAndImmediateInterrupts(t *testing.T) { /* 断言状态与 interrupted_step_seq */ }
func TestRecorderRejectsOutOfOrderStep(t *testing.T) { /* step 2 before 1 -> invalid_arguments */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'Scheduler|Recorder'`
Expected: FAIL。

- [ ] **Step 3: 实现**

Scheduler：2s tick + `Notify`；每 tick 读 paused；取 N 空位；`NextPending`；`SessionPort.Acquire`（失败→blocked/queued）；spawn `CaseRunner.Run`。每 tick 另做两件事：①对 `blocked(session)` 的用例查 `PendingTakeover`/会话状态，接管完成或会话恢复 → 新建 attempt 重排并清理 gate 事件；②`needs_manual` 时发 `human.requested`+`gate.pending`，完成/取消时发 `gate.resolved`。
CaseRunner：建 context（storageState）→ `AgentRuntimePort.StartCaseRun` → 用 `AgentRuntimePort.GetRun` 轮询至终态（超时用 `EZTEST_EXEC_CASE_TIMEOUT_MS`）→ 收尾（close context/lease/release；标记 executed；counts；事件）。步骤边界检查 paused：优雅→pending、立即→interrupted。
Recorder：校验 seq/断言归属/证据存在（`EvidenceRepository`）/verdict（T9）→ 落库 → 事件 → 更新 counts。
**Review Focus #1**：暂停与完成竞态测试（完成前/后暂停各一）。
**Review Focus #5**：分类覆盖测试（env/session/model 不得 failed）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'Scheduler|Recorder'`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/scheduler.go backend/internal/execution/application/case_runner.go backend/internal/execution/application/recorder.go backend/internal/execution/application/*_test.go
git commit -m "feat: add execution scheduler and case runner"
```

### Task 16: 执行 Agent 工具 + profile seed

**Wave 4 · 可并发：是（与 T15、T17、T18；Recorder 接口由 T12 定义）**

**Files:**
- Create: `backend/internal/execution/application/agent_tool/{case_context.go,browser.go,http.go,submit.go,tools.go}`
- Create: `backend/internal/execution/domain/profile.go`
- Create: `backend/internal/execution/infrastructure/agent/runtime.go`
- Test: `backend/internal/execution/application/agent_tool/tools_test.go`
- Test: `backend/internal/execution/domain/profile_test.go`

**Interfaces:**
- Produces: `agent_tool.New(recorder, browser, evidence, httpClient, limits) []domain.Tool`（名字见 spec §7.1）；`domain.ExecutionProfile() agentdomain.Profile`（key `execution-agent`）；`ExecutionPrompt` 常量；`AgentRuntime` 实现 `execution/application.AgentRuntimePort`（`EnsureProfile/StartCaseRun/CancelRun/ListSessions`）。

- [ ] **Step 1: 写失败测试**

```go
func TestBrowserToolReturnsEvidenceIDNotBytes(t *testing.T) {
    rec, br, ev := newToolFixture(t)
    tools := agent_tool.New(rec, br, ev, nil, agent_tool.Limits{})
    out, err := callTool(t, tools, "browser.screenshot", `{}`)
    if err != nil { t.Fatal(err) }
    if strings.Contains(out.Content, "base64") || !strings.Contains(out.Content, "evidence_id") {
        t.Fatalf("artifact must be out-of-band: %s", out.Content)
    }
}
func TestExecutionProfileToolsExist(t *testing.T) {
    p := domain.ExecutionProfile()
    if p.Key != "execution-agent" || len(p.AllowedTools) < 12 { t.Fatalf("%+v", p) }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application/agent_tool ./internal/execution/domain -run 'BrowserTool|ExecutionProfile'`
Expected: FAIL。

- [ ] **Step 3: 实现**

每个工具实现 `agent/domain.Tool`（Name/Description/InputSchema/Execute），从 `Invocation` 取 CaseRunID/BatchID；浏览器工具调 `BrowserExecutor.Action`，artifact 交给 `EvidenceService.Capture` 后只返回 `{evidence_id,sha256,size}`；`http.request` 受白名单（客户端检查）+ 注入 `SessionRef.Headers`（token/apikey/basic）+ 证据脱敏；提交工具调 `Recorder`。profile 常量含 8 点提示词（spec §7.2），`AllowedTools` 与注册名一致。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application/agent_tool ./internal/execution/domain`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/agent_tool backend/internal/execution/domain/profile.go backend/internal/execution/infrastructure/agent
git commit -m "feat: add execution agent tools and profile"
```

### Task 17: 跨上下文适配器

**Wave 4 · 可并发：是（与 T15、T16；适配器只依赖 W1/W2 已完成的上下文方法）**

**Files:**
- Create: `backend/internal/execution/infrastructure/{version,mindmap,resource,eventing,audit}/*.go`
- Test: 同名 `*_test.go`（fake 依赖）

**Interfaces:**
- Produces: `VersionPort/MindmapPort/SessionPort/EventPublisher` 的实现（`AgentRuntimePort` 由 T16 提供）；`audit.Sink` 方法 `BatchCreated/BatchPaused/CaseFinished`（如无既有接口则用 P0 Recorder）。

- [ ] **Step 1: 写失败测试**

```go
func TestMindmapAdapterMapsExecutionCase(t *testing.T) {
    ad := mindmap.New(fakeExecutionService{...})
    b, err := ad.Baseline(ctx, 7)
    if err != nil || len(b.CaseIDs) != 2 { t.Fatalf("%+v %v", b, err) }
}
func TestSessionAdapterReleasesOnNeedsManual(t *testing.T) { /* ErrNeedsManual -> takeover ref */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/...`
Expected: FAIL。

- [ ] **Step 3: 实现**

version 适配 T2；mindmap 适配 T4（`ExecutionCase`→`domain.CaseSnapshot`，ID 字符串化）；resource 适配 T10（`ErrNeedsManual`→`TakeoverRef`，sessionStorage 透传）；agent 适配 T5/P1（Start/Cancel/List/EnsureProfile）；eventing 适配 P0 Publisher（refs.evidence）；audit 适配 P0 Recorder。所有适配器不导入对方 infrastructure。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/infrastructure
git commit -m "feat: wire execution cross-context adapters"
```

---

## Wave 5 — 恢复 / API / 前端壳（T18–T22）

### Task 18: 恢复 worker

**Wave 4 · 可并发：是（与 T15、T16、T17）**

**Files:**
- Create: `backend/internal/execution/application/recovery.go`
- Test: `backend/internal/execution/application/recovery_test.go`

**Interfaces:**
- Produces: `Recovery.Recover(ctx) error`（boot 调用）；`Recovery.Sweep(ctx) error`（可选 ticker）。

- [ ] **Step 1: 写失败测试**

```go
func TestRecoverMarksRunningInterruptedAndPausesBatch(t *testing.T) {
    r, deps := newRecoveryTest(t)
    deps.seedRunning(1, 10); deps.seedRunning(1, 11)
    if err := r.Recover(ctx); err != nil { t.Fatal(err) }
    if deps.runStatus(10) != domain.CaseRunInterrupted || !deps.batchPaused(1) { t.Fatal("recovery wrong") }
    if deps.activeLeases() != 0 { t.Fatal("leases must be released") }
}
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run TestRecover`
Expected: FAIL。

- [ ] **Step 3: 实现**

单事务：running case_run→interrupted（reason restart、cleanup=true、interrupted_step_seq=当前 running step）、batch paused=true/immediate/reason restart、version.MarkPaused、Release 租约；发 `run.paused`/`batch.paused`；幂等（重复执行无额外副作用）。不关闭 executor context（executor 重启自行清理）。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run TestRecover`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/application/recovery.go backend/internal/execution/application/recovery_test.go
git commit -m "feat: add execution crash recovery"
```

### Task 19: HTTP API + DTO + 路由 + 组装根

**Wave 5 · 可并发：否（依赖 T12–T18）**

**Files:**
- Create: `backend/internal/execution/interfaces/httpapi/{handlers_batch.go,handlers_case_run.go,handlers_evidence.go,handlers_report.go,dto.go,routes.go}`
- Modify: `backend/internal/server/server.go`（`Dependencies.Execution` + 挂载）
- Modify: `backend/internal/config/config.go`（P6 env：executor URL/token、evidence dir、并发/预算/上限/worker 间隔）
- Modify: `backend/cmd/eztest/main.go`（构造/seed/Recovery/Scheduler 启动）
- Modify: `backend/internal/version/interfaces/httpapi/dto.go`（版本响应增 `paused/pause_mode/batch_no`；活跃批次由前端从 batches 列表获取）
- Test: `backend/internal/execution/interfaces/httpapi/handlers_test.go`
- Test: `backend/internal/server/execution_routes_test.go`

**Interfaces:**
- Produces: 所有 `/api/versions/{id}/batches*`、`evidence*`、`execution/sessions` 路由；错误信封与 `details.reason`；版本 DTO 加法字段。

- [ ] **Step 1: 写失败测试**

```go
func TestBatchRoutesRequireAuthAndUseStringIDs(t *testing.T) {
    h := newExecutionHandlerTest(t)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req(http.MethodGet, "/api/versions/7/batches", ""))
    if rec.Code != 401 { t.Fatalf("got %d", rec.Code) }
    rec = httptest.NewRecorder()
    h.ServeHTTP(rec, reqWithToken(http.MethodGet, "/api/versions/7/batches", "ezt_user"))
    if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"`) { t.Fatalf("%d %s", rec.Code, rec.Body) }
}
func TestCreateBatchReturns409Reason(t *testing.T) { /* version_not_executable */ }
```

- [ ] **Step 2: 跑失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/interfaces/httpapi ./internal/server`
Expected: FAIL。

- [ ] **Step 3: 实现**

handler 只调 application；DTO 转换 ID 为字符串；`Idempotency-Key` 走既有中间件（为建批/暂停/恢复/取消/重跑注册）；分页 `platform.ParseLimit/Cursor`；证据下载设置 MIME/`X-Content-SHA256`；报告导出 `Content-Disposition`。main.go：构造 repos/services/adapters/tools/registry/profile seed/Recovery/Scheduler（executor 健康探测失败只告警不阻塞启动）；`server.Dependencies.Execution`。
补测试：404/409/422；证据跨版本 404；报告导出头；路由 nil-safe。

- [ ] **Step 4: 跑通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/... ./internal/server`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/execution/interfaces backend/internal/server/server.go backend/cmd/eztest/main.go backend/internal/version/interfaces/httpapi/dto.go backend/internal/server/execution_routes_test.go
git commit -m "feat: expose execution API and wire composition root"
```

### Task 20: 顶栏批次选择 / 暂停恢复 / 横幅

**Wave 6 · 可并发：是（与 T21–T27）**

**Files:**
- Modify: `frontend/src/features/versions/VersionHeader.tsx`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`
- Modify: `frontend/src/features/versions/VersionTabs.tsx`
- Create: `frontend/src/features/execution/components/{BatchSelector,PauseDialog,PausedBanner,StartBatchButton}.tsx`
- Test: `frontend/src/features/execution/components/{PauseDialog.test.tsx,PausedBanner.test.tsx}`

**Interfaces:**
- Consumes: T11 hooks；版本 DTO 新字段。
- Produces: `VersionTabs` 接受 `sessions/exploration/broadcast/results/report` 真实 slot；`VersionDetailPage` 传递它们。

- [ ] **Step 1: 写失败测试**

```tsx
it('immediate pause requires a second confirmation', async () => {
  render(<PauseDialog open mode="immediate" onConfirm={onConfirm} />)
  await user.click(screen.getByTestId('exec-pause-mode-immediate'))
  expect(screen.getByText('该用例将标记 interrupted')).toBeInTheDocument()
  expect(onConfirm).not.toHaveBeenCalled()
})
it('paused banner shows mode and progress', () => { /* ... */ })
```

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/execution/components`
Expected: FAIL。

- [ ] **Step 3: 实现**

Header 在 executing/continuing 显示批次选择与暂停；paused 显示横幅 + 恢复；无活跃批次显示「开始执行」；completed 显示报告入口；暂停/恢复调用 T11 actions 并等事件刷新；`VersionTabs` 删掉 5 个 P6 占位，新增 5 个 slot；`VersionDetailPage` 接入执行 Tab 容器（T21–T25 实现，本任务先传占位空态组件）。保持 issues 占位。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/execution/components src/features/versions`
Expected: PASS（更新既有 VersionDetailPage 断言）。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/versions frontend/src/features/execution/components/BatchSelector.tsx frontend/src/features/execution/components/PauseDialog.tsx frontend/src/features/execution/components/PausedBanner.tsx frontend/src/features/execution/components/StartBatchButton.tsx
git commit -m "feat: add execution controls to version header"
```

### Task 21: 执行会话 Tab + 人工接管卡

**Wave 6 · 可并发：是（与 T20、T22–T27）**

**Files:**
- Create: `frontend/src/features/execution/components/{SessionsTab,HumanGateCard}.tsx`
- Create: `frontend/src/features/execution/components/session-adapter.ts`
- Test: `frontend/src/features/execution/components/{SessionsTab.test.tsx,HumanGateCard.test.tsx}`

**Interfaces:**
- Consumes: `useExecution`、`listExecutionSessions`、agent-console transcript 组件、`useVersionStream`。
- Produces: 会话分组/过滤/transcript/证据内嵌；接管卡（admin 可见完成入口）。

- [ ] **Step 1: 写失败测试**

```tsx
it('groups sessions by agent role and shows current case', () => { /* ... */ })
it('hides takeover completion for normal user', () => { /* role prop */ })
```

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/execution/components/SessionsTab`
Expected: FAIL。

- [ ] **Step 3: 实现**

`session-adapter.ts` 把 P1 session/agent activity 映射为 agent-console 需要的 `AgentEvent`（sessionId/messageId），不修改 agent-console；左栏列表 + 右栏 `Transcript`；顶部过滤/搜索；`stream-status`；证据按钮打开 T22 的 EvidenceViewer（先传回调）；接管卡从 caseRun.pending_takeover 渲染，admin 打开既有 TakeoverDialog。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/execution/components/SessionsTab src/features/execution/components/HumanGateCard`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/execution/components/SessionsTab.tsx frontend/src/features/execution/components/HumanGateCard.tsx frontend/src/features/execution/components/session-adapter.ts frontend/src/features/execution/components/*SessionsTab.test.tsx frontend/src/features/execution/components/HumanGateCard.test.tsx
git commit -m "feat: add execution sessions tab and takeover card"
```

### Task 22: 结果与证据 Tab + 证据查看器

**Wave 6 · 可并发：是（与 T20、T21、T23–T27）**

**Files:**
- Create: `frontend/src/features/execution/components/{ResultsTab,EvidenceViewer,StepDetail,AssertionRows,FailureClassBadge}.tsx`
- Test: `frontend/src/features/execution/components/{ResultsTab.test.tsx,EvidenceViewer.test.tsx}`

**Interfaces:**
- Consumes: `useCaseRuns/useCaseRun`、`getEvidenceUrl`、`getEvidenceMeta`。
- Produces: 结果表 + 步骤/断言展开 + 截图 lightbox + HTTP 双栏 + console 搜索 + 重跑。

- [ ] **Step 1: 写失败测试**

```tsx
it('lists failed cases first and expands steps/assertions', () => { /* ... */ })
it('renders http exchange request and response panes', () => { /* ... */ })
```

- [ ] **Step 2: 跑失败**

Run: `cd frontend; pnpm vitest run src/features/execution/components/ResultsTab`
Expected: FAIL。

- [ ] **Step 3: 实现**

表格失败/阻塞置顶；行展开调用 `getCaseRun`；证据按 kind 渲染（screenshot `<img>`、http JSON 双栏+复制 curl、console 过滤、download 链接）；无 Table/Dialog 原语则手写语义 table 和 modal；重跑按钮调用 action 并刷新。

- [ ] **Step 4: 跑通过**

Run: `cd frontend; pnpm vitest run src/features/execution/components/ResultsTab src/features/execution/components/EvidenceViewer`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/features/execution/components/ResultsTab.tsx frontend/src/features/execution/components/EvidenceViewer.tsx frontend/src/features/execution/components/StepDetail.tsx frontend/src/features/execution/components/AssertionRows.tsx frontend/src/features/execution/components/FailureClassBadge.tsx frontend/src/features/execution/components/*ResultsTab.test.tsx frontend/src/features/execution/components/EvidenceViewer.test.tsx
git commit -m "feat: add execution results and evidence tab"
```

---

## Wave 6 — 其余前端 + fixtures（T23–T27）

### Task 23: 实时播报 Tab

**Wave 6 · 可并发：是（与 T20–T22、T24–T27）**

**Files:**
- Create: `frontend/src/features/execution/components/BroadcastTab.tsx`
- Test: `frontend/src/features/execution/components/BroadcastTab.test.tsx`

- [ ] **Step 1-5:** 测试（倒序/过滤/异常置顶/分页/深链）→ 跑失败 → 实现（复用 `fetchVersionEvents` + `useVersionStream`）→ 跑通过 → `git commit -m "feat: add execution broadcast tab"`。

### Task 24: 探索链路 Tab（手写 SVG）

**Wave 6 · 可并发：是（与 T20–T23、T25–T27）**

**Files:**
- Create: `frontend/src/features/execution/components/{ExplorationTab,ChainGraph}.tsx`
- Create: `frontend/src/features/execution/chain-layout.ts`
- Test: `frontend/src/features/execution/chain-layout.test.ts`、`ExplorationTab.test.tsx`

- [ ] **Step 1-5:** 测试（确定性布局、需求→用例→结果、问题空态、筛选、节点抽屉）→ 跑失败 → 实现（复用 mindmap 布局思路，不引图库；≤200 节点折叠需求）→ 跑通过 → 提交。

### Task 25: 报告 Tab（Batch 1）

**Wave 6 · 可并发：是（与 T20–T24、T26、T27）**

**Files:**
- Create: `frontend/src/features/execution/components/{ReportTab,ReportSummary}.tsx`
- Test: `frontend/src/features/execution/components/ReportTab.test.tsx`

- [ ] **Step 1-5:** 测试（批次切换/摘要/明细/导出触发/空态）→ 跑失败 → 实现（导出走 `apiStreamFetch` + blob 下载；文件名来自头）→ 跑通过 → 提交。

### Task 26: 登录配方配置 UI

**Wave 6 · 可并发：是（与 T20–T25、T27）**

**Files:**
- Modify: `frontend/src/features/resources/**`（账号与访问 Tab：配方列表/编辑/绑定）
- Create: `frontend/src/features/resources/AuthFlowDialog.tsx`
- Test: `frontend/src/features/resources/AuthFlowDialog.test.tsx`

- [ ] **Step 1-5:** 测试（必填/URL 校验/成功规则二选一/admin）→ 跑失败 → 实现（复用既有表单/对话框风格；access 编辑增 login_flow 选择）→ 跑通过 → 提交。

### Task 27: fixture 扩展 + fake LLM P6 模式 + E2E helpers

**Wave 6 · 可并发：是（与 T20–T26）**

**Files:**
- Modify: `scripts/fixture-target.cjs`（`/p6/login`、`/p6/home`、`/p6/orders*`、`/api/p6/orders*`、`/p6/slow`、`/p6/assert-fail`、`/p6/requires-auth`）
- Modify: `scripts/fake-llm.cjs`（`detectP6Scenario`、`handleP6Scenario`、`/__p6-mode`、模式 normal/fail/blocked/takeover/budget）
- Create: `frontend/e2e/p6-helpers.ts`
- Test: `scripts/fake-llm.p6.test.cjs`

- [ ] **Step 1-5:** 测试（fixture 登录/订单/慢响应；fake LLM 工具序列与五种模式）→ 跑失败 → 实现（插在 P5 之后、`recordRequest` 之前；工具名下划线）→ 跑通过（`node --test scripts/fake-llm.p6.test.cjs`）→ 提交。

---

## Wave 7 — E2E / 验收脚本 / OpenAPI（T28–T30）

### Task 28: E2E `execution.spec.ts`

**Wave 7 · 可并发：是（与 T29、T30）**

**Files:**
- Create: `frontend/e2e/execution.spec.ts`
- Modify: `scripts/e2e.ps1`（P6 executor 启动/健康/回收、fixture P6 页面、DB reset 后 seed）

- [ ] **Step 1-5:** 按 spec §10.5 八步写 spec（fake LLM + 真实 executor + fixture）→ 跑 `scripts/e2e.ps1 execution` 失败 → 实现 script/helper/选择器 → 通过 → 提交。
**Review Focus #1/#2**：E2E 必须覆盖暂停/恢复与证据查看器；断言页面不出现密码。

### Task 29: 总验收 P6 区块 + 真实 LLM 脚本

**Wave 7 · 可并发：是（与 T28、T30）**

**Files:**
- Modify: `scripts/acceptance.ps1`（P5 区块后追加 15 个 P6 check）
- Create: `scripts/acceptance-execution-llm.ps1`
- Modify: `.env.example`（P6 段）、`README.md`（P6 runbook）
- Modify: `scripts/test.ps1`（纳入 executor 测试；可选）

- [ ] **Step 1-5:** 先加 check（可先跑出失败/跳过）→ 实现 → 逐项通过 → 提交。
Check 清单严格按 spec §10.6（deliverables/migration/OpenAPI/API smoke/batch/evidence/pause graceful/pause immediate/restart/session login/session invalid/blocked env/classification/report/permissions+degraded）。

### Task 30: OpenAPI 契约

**Wave 7 · 可并发：是（与 T28、T29）**

**Files:**
- Modify: `docs/openapi/api.yaml`
- Test: `scripts/acceptance.ps1` 的 `P6 OpenAPI` check（T29 提供）

- [ ] **Step 1-5:** 先写/更新 path/DTO/事件枚举/错误 reason（19 个）→ 用 `yaml` 解析测试或 acceptance check 失败 → 实现 → 通过 → 提交。

---

## Wave 8 — 文档同步与全量门禁

### Task 31: D39–D42 文档同步 + 全量门禁

**Wave 8 · 可并发：否（依赖全部代码稳定）**

**Files:**
- Modify: `docs/design/system-blueprint.md`（§3.2 D39–D42 + 各章 P6 注记）
- Modify: `Agent.md`（§6.1 目录树、§6.8 证据目录）
- Modify: `docs/design/implementation-phases.md`（§2.9 登录配方行；§2-P7 增 DB 断言；§2-P6 注记）
- Modify: `docs/design/frontend-design.md`（P6 实施注记）
- Modify: `docs/superpowers/specs/2026-10-28-eztest-p6-execution-design.md`（如实现期有偏差，回写）
- Test: `scripts/acceptance.ps1`、`scripts/test.ps1`

- [ ] **Step 1: 更新文档**（逐字写入 D39–D42；表格/scope 同步；不得改 P0–P5 历史结论）。
- [ ] **Step 2: 全量门禁**：`scripts/test.ps1` + `scripts/acceptance.ps1` + `scripts/e2e.ps1 execution` 全绿。
- [ ] **Step 3: 检查未提交/未跟踪**：`git status --short` 仅允许预期文件。
- [ ] **Step 4: 提交** `git commit -m "docs: sync P6 decisions and runbooks"`。

---

## Wave 9 — 真实验收与合回

### Task 32: 真实 LLM 验收 + 真实人工验收 + 合并

**Wave 9 · 可并发：否（Lead 串行）**

**Files:**
- Create/Write: `.superpowers/sdd/2026-10-28-eztest-p6-execution/acceptance.md`（gitignored）
- Evidence: `.tmp/p6-acceptance/`（截图/JSON/报告）

- [ ] **Step 1: 真实 LLM 验收**：`$env:EZTEST_RUN_REAL_LLM="1"; scripts/acceptance-execution-llm.ps1` 全绿；记录耗时/token（可选）。
- [ ] **Step 2: 真实人工验收（Lead 代用户）**：严格按 spec §10.9 八步；真实浏览器 + 真实 LLM + 真实 DB + executor + fixture；每步截图留档。
- [ ] **Step 3: 证据归档**：≥12 截图、DB 行数/关键行、事件快照 JSON、报告 Markdown、运行统计；写 `acceptance.md`（含 DoD 映射与 P6 遗留输入清单）。
- [ ] **Step 4: 失败处理**：任何一条不通过 → 记缺陷、修复、重跑该条及关联回归；不得跳过或降级判定。
- [ ] **Step 5: 合回**：`git switch dev; git merge --ff-only feat/p6-execution`；不 push。
- [ ] **Step 6: 汇报**："通过/不通过 + 证据索引" + 遗留清单。

---

## Self-Review（写完计划后已执行）

1. **Spec coverage**：§1–§15 逐节映射——执行器 T6/T7、DDL T1/T2/T3、引擎 T12/T15、暂停 T15/T18、证据 T13、分类 T9/T15、Session T3/T10、事件 T12/T15/T19、API T19、前端 T11/T20–T26、报告 T14/T25、测试 T27–T29、文档 T31、人工验收 T32。无缺口。
2. **Step scan**：每个任务均为「失败测试→跑失败→实现→跑通过→提交」；实现步骤给签名/文件/取值，不写整段代码（除关键算法与 DDL 指向 spec）。
3. **Type consistency**：`BatchService`/`Recorder`/`BrowserExecutor`/`VersionPort` 等签名在 Shared Interfaces 与各任务中一致；`failure_class` 不含 flaky（派生）；`CaseRunStatus` 8 值、`Verdict` 4 值、`StepStatus` 5 值前后一致。
4. **Review Focus**：5 条均落到任务测试——#1 T15、#2 T7/T13/T16、#3 T12、#4 T10、#5 T9/T15。
5. **Proportion**：计划约 1470 行（spec 1025 行，约 1.4×）；以任务边界/接口/测试断言为主，实现步骤只给签名与取值，不逐行转录程序。
