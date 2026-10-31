# EzTest 0.2 P3（执行/波次/证据/多 SUT）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 打通「current+confirmed 用例 → execution_intent → 自动组波 → 多 SUT Playwright 执行 → 四态结果/证据 → 失败/覆盖分析 → 执行 UI/登录接管」的闭环，冻结 P3 契约并交付 handoff。

**Architecture:** 新建 `backend/internal/execution` 四层限界上下文；确定性 Execution Reconciler/Planner 负责 intent/波次/配额/fencing/offline 重派；API 内 Case Executor Agent（P1 Runner + P3 profile/tools）逐动作驱动独立 Node/Playwright executor；失败/覆盖先确定性兜底、再由 Failure/Coverage Analyst 增强；前端新增 `features/execution`。

**Tech Stack:** Go 1.24.6 + pgx/PostgreSQL 17 + Node 24 + Playwright 1.64.x + Next.js 16/React 19 + Vitest/Playwright + DeepSeek 真实 LLM。

**Spec:** `docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md`（用户已批准；本计划的唯一设计输入）

## Global Constraints

- 严格 4 项 EzTest 启动 env：`EZTEST_DATABASE_URL` / `EZTEST_API_PORT`(13333) / `EZTEST_WEB_PORT`(6868) / `EZTEST_EXECUTOR_PORT`(13336)；**不新增 env**。
- 迁移只追加：`execution_0001_intent_wave.sql`、`execution_0002_result_evidence.sql`、`execution_0003_executor_human.sql`；`order.txt` 第 18–20 个；不改前 17 个。
- 新表雪花 `BIGINT` 主键（应用层生成）；JSON/URL/工具参数 ID 为十进制字符串；跨上下文不建 FK。
- 执行层一律 `version_sut_id`；页面/API/账号/DB 必须属于同一 version_sut；`sut_id` 只展示。
- 四态 `passed/failed/blocked/inconclusive`；`skipped/interrupted/superseded` 过程态；只有 `failed(application)` 产问题候选。
- 配额默认：全局 3（running case_run 数）、每 version_sut 2、跨系统 1；paused 仍占单活跃波。
- 证据字节入 P0 内容账本；单条 ≤ 5 MiB、每 case_run ≤100；API 不暴露路径/凭据/storage_state。
- 失败/覆盖不建 `issue/report/analysis_run`；只写 `case_run.failure_class/summary` + 事件 + 团队帖 + `design.gap`。
- 验收只真实 LLM（DeepSeek `deepseek-flash`）；真实失败不用 fake 替代；单测/集成可用 fake port。
- 每个任务 TDD、独立提交，提交前缀 `type(p3):`；不纳入工作树既有 dirty 文件。
- 不修改 `docs/version-V0.2/**` 设计文档；只改 contracts/handoff/迁移/测试证据；`Agent.md` 仅做 0.1 编号标注。

## Review Focus

1. **重复确认/重复扫描导致的重复执行**：expected one intent + one case_run per `(case_version, env, baseline, purpose)`；pinned by `TestEnsureInitialIntentsIdempotent`（Task 6）与 `TestCreateWaveOnce`（Task 6）。
2. **迟到/旧 epoch 写回覆盖新结果**：expected 409 `lease_lost` 且数据不变；pinned by `TestRecordStepRejectsStaleEpoch`（Task 7）与 `TestExecutorOfflineRedispatch`（Task 9）。
3. **跨系统用例部分占用配额造成死锁**：expected 一次性预留多 SUT slot，不满足则整案跳过；pinned by `TestQuotaReservationCrossSystem`（Task 3）与 `TestSchedulerSkipsCrossSystemWithoutSlots`（Task 6）。
4. **stale/superseded 用例仍在跑**：expected intent/case_run 取消并清理；pinned by `TestSupersededCancelsActive`（Task 9）。
5. **120s 时间窗口被误判/无法等待**：expected `execution.wait` 可等待且页面定时器继续，暂停可中断；pinned by `TestWaitBoundedAndInterruptible`（Task 8）与真实 `-LongWait` 验收（Task 14）。
6. **证据被覆盖/路径或凭据泄露**：expected 不可变、sha 校验、响应无路径/密码；pinned by `TestEvidenceImmutableAndRedacted`（Task 7）与 HTTP 集成（Task 11）。

---

## File Structure（先锁定边界）

```text
backend/internal/execution/
  domain/                 intent.go wave.go case_run.go result.go evidence.go
                          executor.go human.go verdict.go quota.go snapshot.go errors.go
  application/            ports.go fingerprint.go intent.go planner.go scheduler.go
                          case_run.go evidence.go wait.go human.go analysis.go
                          registry.go reconciler.go events.go errors.go
  infrastructure/
    postgres/             repo.go intent.go wave.go case_run.go result.go evidence.go
                          executor.go human.go (integration tests in *_test.go)
    executor/             client.go
    adapters/             design_case_reader.go version_adapter.go credential_adapter.go
                          content_store.go work_item_gap.go team_room.go
  interfaces/httpapi/     handler.go routes.go dto.go views.go
backend/internal/agent/application/
  p3_tools.go             P3 tool wrappers + ExecutionPort
  p3_profiles.go          P3 profile seeds/prompts (or extend profiles.go)
  p3_task_context.go      TaskContextProvider port + ExecutionTask type
  worker.go               RunSink option
  runner.go               P3 task context/tools/scope/writeOutputs
backend/internal/design/application/graph.go   + GetCaseVersion/ListExecutable
backend/internal/version/application/service.go + Reopen
backend/internal/resource/application/service.go + GetAccountSecret/GetDatabaseSecret
backend/internal/server/server.go               + execution deps/routes
backend/cmd/eztest/main.go                      + assembly/poller/scheduler
backend/internal/db/migrations/                 + 3 SQL, order.txt
frontend/src/features/execution/                types.ts api.ts labels.ts ExecutionTab.tsx
                                                WaveList.tsx ResultsPanel.tsx CaseRunDetail.tsx
                                                LiveTrace.tsx EvidenceViewer.tsx HumanRequestBanner.tsx
                                                PausedBanner.tsx PauseDialog.tsx ExecutionToolbar.tsx
                                                PausedBanner.tsx PauseDialog.tsx ExecutionToolbar.tsx
                                                test-fixtures.ts *.test.tsx
frontend/src/features/versions/VersionDetailPage.tsx + execution branch/?wave=
frontend/src/features/clarification/useVersionEvents.ts + execution events
frontend/src/lib/format.ts                      + formatDuration
executor/src/{server.mjs,contexts.mjs}          + metadata/GET /v1/contexts
scripts/fixture-target.cjs                      + /p3/* two variants
scripts/p3-fixture-db.ps1                       new
scripts/p3-acceptance.ps1                       new (real LLM)
scripts/p3-e2e.ps1                              new
frontend/e2e/p3.spec.ts                         new
scripts/p3-contract-check.cjs                   new
scripts/dev-executor.ps1                        read token from DB
docs/version-V0.2/contracts/{openapi.yaml,events.md,db-schema.md,
  migrations-order.md,task-types.md,agent-profiles-tools.md,settings.md,
  executor-protocol.md,README.md}               P3 increments + freeze
docs/version-V0.2/phases/phase-03-handoff.md    new
Agent.md                                        0.1 phase labels
```

---

### Task 0: Baseline + Agent.md 0.1 labeling + branch hygiene

**Files:**
- Modify: `Agent.md`（§6.1 段首、P1–P7 段落、§6.5/§6.7/§6.8/§6.9 的 P 编号）
- Verify: `scripts/test.ps1` baseline

**Interfaces:**
- Produces: 干净的 P3 基线；后续任务以此分支/证据为准。

- [ ] **Step 1: 跑 P2 基线门禁**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1 2>&1 | Tee-Object .tmp/p3/baseline-test.log`
Expected: `EXIT=0`；Go 全 ok；前端 54 files / 413+ tests。若失败 → 停止并报告，不继续。

- [ ] **Step 2: 标注 Agent.md 的 0.1 编号**

在 `Agent.md` §6.1 开头加：

```markdown
> **阶段编号说明（2026-11-01）**：本节目录树与 P1–P7 段落中的 P 编号指 **EzTest 0.1** 的阶段（`docs/design/system-blueprint.md` 时代），**不是** `docs/version-V0.2` 的 P0–P8 实施阶段。0.2 的阶段定义见 `docs/version-V0.2/implementation-phase-plan.md`；本节相关段落已加 `0.1-` 前缀。
```

把 §6.1 的 “P1 新增的 agent 上下文” … “P7 新增的 delivery 上下文” 六个段首改为 `0.1-P1…0.1-P7`；§6.1 目录树中 `# 限界上下文：…（P1）` 等改为 `（0.1-P1）`；§6.5/§6.7/§6.8/§6.9 的 P 编号同样加 `0.1-`。

- [ ] **Step 3: 验证标注与提交**

Run: `git diff --check Agent.md`；`git diff --stat Agent.md`
Expected: 只改文字，无空白错误；提交：

```powershell
git add Agent.md
git commit -m "docs(p3): label Agent.md phase numbering as 0.1"
```

---

### Task 1: P3 数据契约 + 3 个迁移（只追加）

**Files:**
- Create: `backend/internal/db/migrations/execution_0001_intent_wave.sql`
- Create: `backend/internal/db/migrations/execution_0002_result_evidence.sql`
- Create: `backend/internal/db/migrations/execution_0003_executor_human.sql`
- Modify: `backend/internal/db/migrations/order.txt`（追加 3 行）
- Modify: `backend/internal/db/migrate_test.go`（17→20）
- Modify: `backend/internal/db/migrate_integration_test.go`（want 20）
- Modify: `docs/version-V0.2/contracts/db-schema.md`（§9，10 表）
- Modify: `docs/version-V0.2/contracts/migrations-order.md`（§5c）
- Create/Modify: `scripts/p3-contract-check.cjs`（数据段断言）

**Interfaces:**
- Produces: 表/列/约束命名按 Spec §3；后续 repo 使用这些表。

- [ ] **Step 1: 先写失败的迁移集成测试**

在 `backend/internal/db/migrate_integration_test.go` 增加：

```go
func TestP3ExecutionTables(t *testing.T) {
    // 使用 testutil.NewDB 跑全量迁移后查询 information_schema.tables，
    // 断言 execution_intent/run_wave/case_run/case_run_sut_context/
    // step_result/assertion_result/evidence/evidence_ref/executor_instance/human_request 存在；
    // 断言 uk_execution_intent_active/uk_run_wave_active/uk_case_run_wave_intent 存在。
}
```

并把 `want := 17` 改为 `want := 20`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/... -run TestP3ExecutionTables -v`
Expected: FAIL（表不存在/迁移数 17）。

- [ ] **Step 3: 写 3 个迁移与 order.txt**

按 Spec §3.1–§3.4 逐列/约束/索引写入；`order.txt` 末尾追加三个文件名。注意：
`execution_intent` 用 partial unique `uk_execution_intent_active`，`dedupe_key` 只建普通索引；
`run_wave` 用 partial unique `uk_run_wave_active`；
`case_run` 用 `uk_case_run_wave_intent`；
`evidence` 只 `created_at`（不可变）；
`human_request.case_run_id` 可空、逻辑引用。

- [ ] **Step 4: 更新契约文档 + 契约检查数据段**

`db-schema.md` 新增 `## 9. P3 增量表（frozen: P3）`，逐表列/PK/UK/IDX/枚举/不变量；`migrations-order.md` 新增 `## 5c. P3 增量迁移`。`scripts/p3-contract-check.cjs` 先支持 `--draft` 模式：断言迁移文件名、10 表名出现在 db-schema、order.txt 含 3 行。先写断言后写入文档。

- [ ] **Step 5: 跑测试与契约检查**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/... -run 'TestP3ExecutionTables|TestMigrations' -v`
Expected: PASS。
Run: `node scripts/p3-contract-check.cjs --draft`
Expected: 输出 `P3 data contracts present`，EXIT=0。

- [ ] **Step 6: 提交**

```powershell
git add backend/internal/db/migrations backend/internal/db/migrate_test.go backend/internal/db/migrate_integration_test.go docs/version-V0.2/contracts/db-schema.md docs/version-V0.2/contracts/migrations-order.md scripts/p3-contract-check.cjs
git commit -m "feat(p3): add execution tables and migrations"
```

---

### Task 2: P3 API/事件/任务/Profile/设置/executor 协议契约（阶段内 draft）

**Files:**
- Modify: `docs/version-V0.2/contracts/openapi.yaml`（22 paths + schemas；`info.version: 0.2.0-p3`；`x-draft: P3`）
- Modify: `docs/version-V0.2/contracts/events.md`（§12，20 事件）
- Modify: `docs/version-V0.2/contracts/task-types.md`（§5 + §4 移出）
- Modify: `docs/version-V0.2/contracts/agent-profiles-tools.md`（§6 + §4b 移出）
- Modify: `docs/version-V0.2/contracts/settings.md`（49 键 + §4c）
- Create: `docs/version-V0.2/contracts/executor-protocol.md`
- Modify: `docs/version-V0.2/contracts/README.md`（目录 + P3 段占位）
- Modify: `scripts/p3-contract-check.cjs`（API/events/tasks/profiles/settings/protocol 断言）

**Interfaces:**
- Produces: 后续后端/前端/executor 任务按此契约实现；22 paths 见 Spec §9.2，20 事件见 §10.1。

- [ ] **Step 1: 扩展契约检查为失败测试**

在 `scripts/p3-contract-check.cjs` 增加断言：
`/api/versions/{id}/execution/overview`、`/waves`、`/waves/{waveId}/pause|resume|cancel`、`/case-runs/{caseRunId}`、`/evidence/{evidenceId}/content`、`/human-requests/{humanRequestId}/resolve`、`/api/admin/executors/{executorId}/drain` 等 22 paths；`execution_intent_created`… 等 20 事件串；`execute.case`/`analyze.failure`/`analyze.coverage`；`case_executor`/`failure_analyst`/`coverage_analyst`；`execution.executor_token` 等 5 键；`executor-protocol.md` 文件存在。

Run: `node scripts/p3-contract-check.cjs --draft`
Expected: FAIL（缺 path/schema/事件等）。

- [ ] **Step 2: 写 db 接口契约**

按 Spec §9/§10 逐项补 `openapi.yaml`：tags、22 paths、请求/响应 schema（`ExecutionOverview`、`RunWaveView`、`CaseRunView`、`CaseRunDetail`、`EvidenceView`、`HumanRequestView`、`IntentView`、`ExecutorView`、`PauseWaveRequest`、`CreateIntentRequest`、`ResolveHumanRequestRequest`、`ListEnvelope` 复用）；错误码复用 P0，新增 `evidence_too_large`/`executor_unavailable` 描述。

- [ ] **Step 3: 写事件/任务/Profile/设置/协议契约**

`events.md` §12 20 事件（含 `human_request.expired`）；`task-types.md` §5 三任务（§4 移出）；`agent-profiles-tools.md` §6 三 profiles + 工具 schema（含 `execution.wait`、`browser.*`、`http.request`、`db.query`、`execution.*`、`human.request`、`account.credentials`、`database.schema_read`、`analysis.*`）；`settings.md` §3 44→49 与 §4c；`executor-protocol.md` 按 Spec §6.1/§6.2/§6.10（pull 注册、token env、metadata、GET /v1/contexts、错误码/限额、P7 保留）。

- [ ] **Step 4: 跑契约检查与 YAML 解析**

Run: `node scripts/p3-contract-check.cjs --draft`
Expected: PASS，输出 paths/ops/schemas/events/tables/migrations 计数。
Run: `node -e "const yaml=require('./frontend/node_modules/yaml');const fs=require('fs');yaml.parse(fs.readFileSync('docs/version-V0.2/contracts/openapi.yaml','utf8'));console.log('yaml ok')"`
Expected: `yaml ok`。

- [ ] **Step 5: 提交**

```powershell
git add docs/version-V0.2/contracts scripts/p3-contract-check.cjs
git commit -m "docs(p3): draft execution contracts"
```

---

### Task 3: execution 领域纯逻辑（状态机/四态聚合/配额/类型）

**Files:**
- Create: `backend/internal/execution/domain/{intent.go,wave.go,case_run.go,result.go,evidence.go,executor.go,human.go,snapshot.go,verdict.go,quota.go,errors.go}`
- Create: `backend/internal/execution/domain/domain_test.go`（或分文件 `*_test.go`）

**Interfaces:**
- Produces（后续任务精确使用）：
  - `type IntentStatus string`；常量 `IntentOpen/Claimed/Running/Done/Failed/Cancelled/Superseded`；`func (s IntentStatus) Active() bool`；`func (s IntentStatus) CanTransitionTo(next IntentStatus) bool`。
  - `type Purpose string`；`PurposeInitial/Retest/Manual`。
  - `type ExecutionIntent struct { ID, VersionID, CaseVersionID int64; VersionSUTIDs []int64; EnvironmentFingerprint, CodeBaseline string; Purpose Purpose; Status IntentStatus; DedupeKey string; AttemptNo int; CreatedBy int64; CreatedAt, UpdatedAt time.Time }`。
  - `type WaveStatus string`；`WavePlanned/Running/Paused/Done/Failed/Cancelled`；`type WaveCounts struct { Total, Pending, Running, Paused, Passed, Failed, Blocked, Inconclusive, Skipped, Interrupted, Superseded, Evidence int; ByVersionSUT map[string]SUTCounts }`；`type RunWave struct { ID, VersionID int64; Status WaveStatus; Trigger string; Counts WaveCounts; StartedAt, FinishedAt, PausedAt *time.Time; PausedBy int64; PauseReason, PauseMode string; ReportID *int64; CreatedAt, UpdatedAt time.Time }`。
  - `type CaseRunStatus string`；常量 `CaseRunPending/Running/Paused/Passed/Failed/Blocked/Inconclusive/Skipped/Interrupted/Superseded`；`Terminal()/Active()`。
  - `type FailureClass string`；9 值常量。
  - `type CaseRun struct { ID, WaveID, IntentID, CaseVersionID int64; Status CaseRunStatus; Attempts int; FailureClass FailureClass; Summary CaseRunSummary; StartedAt, FinishedAt *time.Time; OwnerInstance string; OwnerExecutorID *int64; DispatchEpoch int64; DispatchToken string; LeaseUntil *time.Time; PausedAt *time.Time; PausedBy *int64; PauseReason string }`。
  - `type CaseRunSummary struct { VerdictReason string; AssertionTotal, AssertionFailed, StepTotal, StepFailed, EvidenceCount, VisualSkipped int; HumanRequestID int64; LastError string }`。
  - `type StepStatus string`；`type StepResult struct { ID, CaseRunID int64; Seq int; TargetVersionSUTID *int64; Status StepStatus; Observed map[string]any; Error string; StartedAt, FinishedAt *time.Time }`。
  - `type AssertionVerdict string`；`type AssertionResult struct { ID, CaseRunID int64; StepSeq, AssertionSeq int; Kind string; Verdict AssertionVerdict; Reason, Expected, Actual string; TargetVersionSUTID *int64 }`。
  - `type CaseStep struct { Seq int; Action, Expected string; TargetVersionSUTID int64; AccountRole string }`；`type CaseAssertion struct { StepSeq, AssertionSeq int; Kind, Expected string; TargetVersionSUTID int64 }`；`type CaseSnapshot struct { CaseID, CaseVersionID, RequirementID, RequirementVersionID, DefaultVersionSUTID int64; Title, Description, Preconditions, Priority string; VersionSUTIDs []int64; Steps []CaseStep; Assertions []CaseAssertion }`。
  - `type FinishCandidate struct { Verdict CaseRunStatus; Summary string; FailureClass FailureClass }`；`type SUTBinding struct { VersionSUTID int64; SUTName, EnvironmentName, BaseURL string; PageURLs, APIEndpoints []string; AccountRoles []string }`；`type TaskSnapshot struct { Case CaseSnapshot; Intent ExecutionIntent; CaseRun CaseRun; SUTs []SUTBinding; PriorSteps []StepResult; PriorAssertions []AssertionResult; HumanResponse string }`。
  - `type VerdictInput struct { Case CaseSnapshot; Steps []StepResult; Assertions []AssertionResult; Finish *FinishCandidate; AgentFailure FailureClass }`；`func Aggregate(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error)`。
  - `type Quota struct { Global, PerSUT, CrossSystem int }`；`type RunningCase struct { VersionSUTIDs []int64; CrossSystem bool }`；`func CanDispatch(q Quota, running []RunningCase, candidate RunningCase, execCapacity, execActive, candidateContexts int) (bool, string)`。
  - `type EvidenceKind string`；8 值；`type Evidence struct {...}`；`type HumanRequestStatus/Kind`。
- Consumes: `platform.Error/Invalid/StateConflict`（按现有风格）。

- [ ] **Step 1: 写失败测试（状态机/聚合/配额）**

在 `domain_test.go` 写表驱动：
- `TestIntentTransitions`：open→claimed→running→done 合法；done→running 非法；cancelled 幂等。
- `TestAggregateFourStates`：断言失败→failed/application；step 工具错误→failed/automation；缺结果→inconclusive；全通过→passed；无法开始→blocked；visual skipped 不判 passed。
- `TestQuotaCrossSystemReservation`：全局 3/每 SUT 2/跨系统 1；cross-system 候选需 2 SUT 且 2 slots 都可用才 true；只有 1 个 slot→false；exec capacity 不足→false；reason 固定字符串。
- `TestFailureClassOnlyApplicationCandidate`：`application` true，其余 false（辅助 `IsIssueCandidate`）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/domain -v`
Expected: FAIL（类型/函数未定义）。

- [ ] **Step 3: 实现 domain 类型与纯函数**

按 Spec §4.1–§4.6、§5.3；`Aggregate` 严格按 §4.3 优先级：blocked > failed(step/assertion) > passed > inconclusive；expected 不由模型决定由调用方覆写；visual 断言只 skipped。

- [ ] **Step 4: 跑测试通过 + 覆盖**

Run: `cd backend; ..\.tools\go\bin\go.exe test -cover ./internal/execution/domain`
Expected: PASS，`coverage` ≥ 85%。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution/domain
git commit -m "feat(p3): add execution domain state machines and verdicts"
```

---

### Task 4: execution PostgreSQL 仓储（10 表）

**Files:**
- Create: `backend/internal/execution/application/ports.go`（仓储接口）
- Create: `backend/internal/execution/infrastructure/postgres/{repo.go,intent.go,wave.go,case_run.go,result.go,evidence.go,executor.go,human.go}`
- Create: `backend/internal/execution/infrastructure/postgres/repo_integration_test.go`（`//go:build integration`）

**Interfaces:**
- `type IntentRepository interface { Create(ctx, domain.ExecutionIntent) error; Get(ctx, id int64) (domain.ExecutionIntent, error); List(ctx, versionID int64, status string, limit int, beforeID *int64) ([]domain.ExecutionIntent, bool, error); ListOpen(ctx, versionID int64) ([]domain.ExecutionIntent, error); Transition(ctx, id int64, from []domain.IntentStatus, to domain.IntentStatus) (bool, error); CountInitialFor(ctx, caseVersionID int64, fingerprint, baseline string) (int, error) }`
- `type WaveRepository interface { CreateWithCaseRuns(ctx, domain.RunWave, []domain.ExecutionIntent, []domain.CaseRun) error; Get(ctx, id int64) (domain.RunWave, error); GetActive(ctx, versionID int64) (*domain.RunWave, error); List(ctx, versionID int64, limit int, beforeID *int64) ([]domain.RunWave, bool, error); Update(ctx, domain.RunWave) (bool, error); Counts(ctx, waveID int64) (domain.WaveCounts, error) }`
- `type CaseRunRepository interface { Get(ctx,id int64)(domain.CaseRun,error); ListByWave(ctx,waveID int64,status string,limit int,beforeID *int64)([]domain.CaseRun,bool,error); Create(ctx,domain.CaseRun) error; UpdateCAS(ctx, id, epoch int64, token string, mutate func(*domain.CaseRun) error) (bool,error); Transition(ctx,id int64,from []domain.CaseRunStatus,to domain.CaseRunStatus,epoch int64,token string)(bool,error); ClaimLease(ctx,id int64,epoch int64,until time.Time)(bool,error); ListRunningByExecutor(ctx,executorID int64)([]domain.CaseRun,error) }`
- `type ResultRepository interface { UpsertStep(ctx, domain.StepResult, epoch int64, token string) (bool,error); UpsertAssertion(ctx, domain.AssertionResult, epoch int64, token string) (bool,error); ListSteps(ctx, caseRunID int64) ([]domain.StepResult,error); ListAssertions(ctx, caseRunID int64) ([]domain.AssertionResult,error) }`
- `type EvidenceRepository interface { Insert(ctx, domain.Evidence) error; Get(ctx,id int64)(domain.Evidence,error); ListByCaseRun(ctx,caseRunID int64,limit int)([]domain.Evidence,error); ListByVersion(ctx,versionID int64,f EvidenceFilter)([]domain.Evidence,bool,error); InsertRef(ctx, domain.EvidenceRef) error; CountByCaseRun(ctx,caseRunID int64)(int,error) }`
- `type ExecutorRepository interface { UpsertHeartbeat(ctx, domain.ExecutorInstance) error; GetByEndpoint(ctx,endpoint string)(domain.ExecutorInstance,error); List(ctx)([]domain.ExecutorInstance,error); SetStatus(ctx,id int64,status domain.ExecutorStatus)(bool,error) }`
- `type HumanRequestRepository interface { Create(ctx,domain.HumanRequest) error; Get(ctx,id int64)(domain.HumanRequest,error); GetOpenByCaseRun(ctx,caseRunID int64)(*domain.HumanRequest,error); ListByVersion(ctx,versionID int64,status string,limit int,beforeID *int64)([]domain.HumanRequest,bool,error); Transition(ctx,id int64,from,to domain.HumanRequestStatus,response map[string]any,actorID int64)(bool,error); Expire(ctx,now time.Time)(int64,error) }`
- All methods take `context.Context`; IDs int64; errors: `platform.Error{Code:"not_found"}` etc.

- [ ] **Step 1: 写失败的集成测试**

`repo_integration_test.go` 用 `testutil.NewDB(t)` 建 schema/迁移；测试：
- `TestIntentActiveUnique`：同键插两个 active → 第二个冲突；插 done 后再插 active 成功。
- `TestWaveActiveUnique`：同 version 两个 active → 冲突；done 后可再建。
- `TestCaseRunUniqueAndCAS`：同 `(wave,intent)` 冲突；`UpdateCAS` 错 epoch/token 返回 false 且不改行。
- `TestStepAssertionUpsert`：同 `(case_run,seq)` 两次 upsert 后 1 行、值更新；延迟 epoch 拒绝。
- `TestExecutorUpsertAndHumanTransition`：endpoint upsert；human open→resolved CAS；expired 扫描。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration ./internal/execution/infrastructure/postgres -run TestIntentActiveUnique -v`
Expected: FAIL（包/表/方法不存在）。

- [ ] **Step 3: 实现 repo（pgx）**

按 Spec §3 表；SQL 显式列名；所有更新带 `WHERE ... AND status = ANY(...)` 或 epoch/token；`List` 倒序 `id < beforeID`；`Counts` 用 `FILTER (WHERE status=...)` 单查询 + `by_version_sut` 从 `case_run_sut_context` join 统计（跨系统用例在每个 SUT 计数，total 不去重求和）。错误映射：无行 → `not_found`；唯一冲突 → `conflict`；CAS 失败 → `false,nil`。

- [ ] **Step 4: 跑集成测试通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/execution/infrastructure/postgres -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution/application/ports.go backend/internal/execution/infrastructure/postgres
git commit -m "feat(p3): add execution postgres repositories"
```

---

### Task 5: executor 客户端 + 注册/心跳/容量 + executor 协议扩展

**Files:**
- Create: `backend/internal/execution/infrastructure/executor/client.go`
- Create: `backend/internal/execution/application/registry.go`
- Create: `backend/internal/execution/infrastructure/executor/client_test.go`（httptest）
- Modify: `executor/src/server.mjs`、`executor/src/contexts.mjs`、`executor/test/server.test.mjs`、`executor/test/contexts.test.mjs`
- Modify: `docs/version-V0.2/contracts/executor-protocol.md`（Task 2 已创建，此处校准）

**Interfaces:**
- `type Client interface { Health(ctx) (Health,error); CreateContext(ctx,CreateContextInput)(ContextInfo,error); Action(ctx,contextID,action string,args json.RawMessage)(ActionResult,error); Cancel(ctx,contextID string)(bool,error); Close(ctx,contextID string) error; ListContexts(ctx)([]ContextInfo,error) }`
- `type Health struct { Status string; Version string; Contexts int; MaxContexts int; BrowserVersion string; Error string }`
- `type CreateContextInput struct { Key string; AllowedHosts []string; BaseURL string; StorageState json.RawMessage; Metadata map[string]any; Viewport *Viewport }`
- `type ContextInfo struct { ID string; Key string; Metadata map[string]any; CreatedAt string }`
- `type ActionResult struct { OK bool; Result json.RawMessage; Artifacts []Artifact; Error *ActionError }`；`type Artifact struct { Kind,MIME,Base64,SHA256,Filename string; Bytes int }`
- `func NewClient(endpoint, token string, timeout time.Duration) *HTTPClient`
- Registry：`type Registry struct { ... }`；`func (r *Registry) PollOnce(ctx) error`；`func (r *Registry) Select(ctx) (*domain.ExecutorInstance, error)`；`func (r *Registry) Drain/Activate(ctx,id)`；常量 interval 从 settings 读。

- [ ] **Step 1: 写失败的 exec 客户端测试**

httptest 断言：`Authorization: Bearer <token>`；`POST /v1/contexts` body 含 key/allowedHosts/baseUrl/metadata；`GET /v1/contexts` 解析；非 2xx → typed error；`healthz` 无 auth。`TestRegistryPollUpsertsAndOffline`：先健康 → active；再 500 + 超 offline → offline；draining 保持。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/executor ./internal/execution/application -run TestClient -v`
Expected: FAIL。

- [ ] **Step 3: 实现 client/registry**

按 `executor/README.md` 与 Spec §6.1/§6.2；`GET /v1/contexts` 为 P3 扩展；healthz 失败时更新 `last_error`，超时判 offline（`now-last_heartbeat > offline_seconds`）；`token_hash=sha256(token)`；`Select` 只选 active 且 endpoint 匹配；`Drain/Activate` 改状态。

- [ ] **Step 4: executor Node 扩展 + 测试**

`server.mjs`：`POST /v1/contexts` 接受 `metadata`（object ≤2KB，非 object→400）；`GET /v1/contexts`（Bearer）返回 `{contexts:[{id,key,metadata,createdAt}]}`；`contexts.mjs` 保存 metadata/createdAt，提供 `List()`。测试：创建带 metadata → list 返回；超 2KB 拒绝；无 token 401。

Run: `cd executor; pnpm test`
Expected: PASS（89+ 新测试）。

- [ ] **Step 5: 跑 Go 测试并提交**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/infrastructure/executor ./internal/execution/application -v`
Expected: PASS。

```powershell
git add backend/internal/execution/infrastructure/executor backend/internal/execution/application/registry.go executor/src executor/test
git commit -m "feat(p3): add executor client and registry heartbeat"
```

---

### Task 6: Intent 生成 + Wave Planner/调度 + 版本重开

**Files:**
- Create: `backend/internal/execution/application/{fingerprint.go,errors.go,events.go,planner.go,scheduler.go}`
- Create: `backend/internal/execution/application/{fingerprint_test.go,planner_test.go,scheduler_test.go}` + fakes
- Modify: `backend/internal/design/application/graph.go`（`GetCaseVersion`/`ListExecutable`）
- Modify: `backend/internal/version/application/service.go`（`Reopen`）

**Interfaces:**
- `type CaseReader interface { GetCaseVersion(ctx, versionID, caseVersionID int64) (domain.CaseSnapshot, error); ListExecutable(ctx, versionID int64) ([]domain.CaseSnapshot, error) }`
- `type VersionReader interface { Get(ctx, id int64) (VersionBinding, error); Reopen(ctx, id int64) error }`；`type VersionBinding struct { VersionID int64; Status string; SUTs []VersionSUTBinding }`；`type VersionSUTBinding struct { VersionSUTID, SUTID, EnvironmentID int64; SUTName, EnvironmentName, BaseURL string; PageIDs, APIIDs, DatabaseIDs []int64; AccountRoles []string; CodeRepoID *int64; Branch string }`
- `type IntentService struct { ... }`；`func (s *IntentService) EnsureInitialIntents(ctx, versionID int64) (int, error)`；`func (s *IntentService) CreateManual(ctx, versionID, caseVersionID int64, purpose domain.Purpose, actorID int64) (domain.ExecutionIntent, error)`；`func (s *IntentService) Cancel(ctx, intentID, actorID int64) error`
- `type Scheduler struct { ... }`；`func (s *Scheduler) Reconcile(ctx) error`；`func (s *Scheduler) ReconcileVersion(ctx, versionID int64) error`
- `func EnvironmentFingerprint(b VersionBinding) (string, error)`；`func CodeBaseline(b VersionBinding) string`；`func DedupeKey(...) string`
- `Scheduler` deps: `IntentRepository`, `WaveRepository`, `CaseRunRepository`, `CaseReader`, `VersionReader`, `ExecutorRegistry`, `WorkQueue`, `IDGenerator`, `Clock`, `Publisher`, `Audit`, `QuotaReader`.

- [ ] **Step 1: 写失败的测试**

- `TestEnvironmentFingerprintStableAndChanges`：同绑定两次相同；页面/环境/账号变化不同；排序无关。
- `TestEnsureInitialIntentsIdempotent`：两个 current+confirmed case → 2 intents；再跑 → 0 新增；同 case 第二个 initial → 0。
- `TestManualIntentRejectsActiveDuplicate`：active 同键 → `state_conflict`；done 后 manual 成功。
- `TestCreateWaveAndClaimIntents`：无 active wave → 1 wave + 每 intent 1 case_run + intent claimed；再跑不重复。
- `TestSchedulerQuotaAndCrossSystem`：全局 3/per SUT 2/cross 1；候选跨系统需 2 slots；不满足整案跳过，后续单 SUT 可派发。
- `TestReopenCompletedVersionOnIntent`：version completed → create intent 调 Reopen。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'TestEnsureInitialIntents|TestCreateWave|TestScheduler|TestEnvironmentFingerprint|TestManualIntent' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 fingerprint/intent/planner/scheduler**

按 Spec §5.1–§5.6。要点：
- `EnsureInitialIntents` 只对 current+confirmed；若 `CountInitialFor>0` → 跳过；先 `Version.Reopen`（如 completed）再创建。
- `CreateManual` 校验 current+confirmed；active 同键 → 409；purpose 只 retest/manual。
- `Scheduler.Reconcile`：扫描 `version` 的 intents；每 version 单 active wave；创建 wave+case_runs+claimed 同一事务；派发 pending（配额/executor capacity）；wave 终态时 intent done/failed、counts、enqueue analyze（通过 `WorkQueue.Ensure`，dedupe `failure:{waveID}`/`coverage:{waveID}`）。
- `ReconcileVersion` 支持单版本（测试/API 触发）。
- 版本完成→Reopen：`versionapp.Service.Reopen` 清 `closed_at/close_reason`、状态 completed→running、发 `version.status_changed`、审计；仅 completed 生效，其他幂等。

- [ ] **Step 4: 跑测试通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application ./internal/design/application ./internal/version/application -v`
Expected: PASS（含现有 P1/P2 测试）。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution/application backend/internal/design/application/graph.go backend/internal/version/application/service.go
git commit -m "feat(p3): plan intents, waves and quota scheduling"
```

---

### Task 7: CaseRun 生命周期 + 多 SUT 上下文 + 证据/等待/DB/HTTP 工具服务

**Files:**
- Create: `backend/internal/execution/application/{case_run.go,evidence.go,wait.go,credentials.go}`
- Create: `backend/internal/execution/application/{case_run_test.go,evidence_test.go,wait_test.go}`
- Create: `backend/internal/execution/infrastructure/adapters/{content_store.go,credential_adapter.go,design_case_reader.go,version_adapter.go}`
- Modify: `backend/internal/resource/application/service.go`（`GetAccountSecret/GetDatabaseSecret`）

**Interfaces:**
- `type CaseRunner struct { ... }`
  - `func (s *CaseRunner) Prepare(ctx, workItem agent.WorkItem) (TaskSnapshot, error)`：校验 case_run pending/running、intent active、case current+confirmed；置 running/lease/epoch/token；创建 `case_run_sut_context` 行（无 browser id）；返回任务快照（case/steps/assertions/bindings/prior results/human response）。
  - `func (s *CaseRunner) RecordStep(ctx, scope tools.Scope, in StepRecordInput) (any, error)`
  - `func (s *CaseRunner) RecordAssertion(ctx, scope, in AssertionRecordInput) (any, error)`
  - `func (s *CaseRunner) AddEvidence(ctx, scope, in EvidenceInput) (domain.Evidence, error)`
  - `func (s *CaseRunner) Wait(ctx, seconds int, reason string) (WaitResult, error)`
  - `func (s *CaseRunner) Finish(ctx, scope, candidate FinishCandidate) (domain.CaseRun, error)`
  - `func (s *CaseRunner) Finalize(ctx, caseRunID int64, runErr error, attempt, maxAttempts int) error`
  - `func (s *CaseRunner) RenewLease(ctx, caseRunID, epoch int64, token string) error`
- `type CredentialReader interface { Account(ctx, versionSUTID int64, role string) (AccountCredential, error); Database(ctx, versionSUTID int64, databaseID *int64) (DatabaseCredential, error) }`
- `resourceapp.Service.GetAccountSecret(ctx, accountID int64) (username,password string,error)`；`GetDatabaseSecret(ctx,databaseID int64)(dsn,username,password string,error)`（application 方法；不新增 HTTP）。

- [ ] **Step 1: 写失败的测试**

- `TestPrepareRejectsNonCurrentCase` / `TestPrepareSetsRunningLease`：current+confirmed 通过；stale → state_conflict。
- `TestRecordStepRejectsStaleEpoch`：错 epoch/token → `lease_lost` 且行不变；正确 upsert。
- `TestRecordAssertionExpectedOverridden`：提交 expected=fake → 存储用 P2 expected；assertion_seq 越界 → invalid_argument。
- `TestAggregateOnFinish`：断言失败 → failed/application；全通过 → passed；缺断言 → inconclusive。
- `TestEvidenceImmutableAndMaxBytes`：超 5MiB 拒绝 `evidence_too_large`；100 条后拒绝/跳过；sha 重算；下载前校验。
- `TestWaitBoundedAndInterruptible`：ctx cancel 立即返回；>300 拒绝；累计超 case timeout 拒绝。
- `TestCredentialsRoleResolution`：role 精确匹配；无账号 → not_found；密码只经内存。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'TestPrepare|TestRecord|TestAggregate|TestEvidence|TestWait|TestCredentials' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 CaseRunner/Evidence/Wait/Credentials + adapters**

按 Spec §4.3/§6.4/§6.10/§6.5/§6.7。要点：
- `Prepare` 不创建 browser context（懒创建）；写 `case_run_sut_context` 每个 `(version_sut,role)` 一行；任务快照含 `PriorSteps/PriorAssertions` 从 repo 读；human_response 从 open/resolved human 读一次并标记 consumed（用 payload 临时字段或 summary）。
- `RecordStep/Assertion` 先 CAS epoch/token，再 upsert；assertion expected 从 case snapshot 覆写；发 `case_run.step_finished/assertion_finished`。
- `AddEvidence`：雪花 ID → `ContentStore.Upload` → `AddRef` → insert evidence+ref；size/MIME/sha 由 API 重算；超限错误。
- `Wait`：`time.NewTimer`/ctx；检查 case 状态（paused/cancelled/lease）每 1s？用 ctx only; pause handler cancels runner context via worker? Pause service calls a run canceller registry. Add `RunCanceller` port; `Wait` just selects ctx/timer; pause/cancel cancels by calling `CancelRun(caseRunID)` in scheduler/human service. Implement `activeRuns map[int64]context.CancelFunc` in CaseRunner with `Register/Cancel`.
- `Finish`：候选 + Aggregate；覆写不一致；写 summary/status/finished；发事件；清理 context（best-effort）；release 配额；update counts；标记 intent done/failed；ensure analyze tasks.
- `Finalize`：worker sink 调用；runErr retryable 且 attempt<max → 只记 last_error，不终态；否则失败 class model/automation/timeout；成功但未 finish → Aggregate 兜底。

- [ ] **Step 4: 跑测试通过 + 集成**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/... ./internal/resource/... -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution/application backend/internal/execution/infrastructure/adapters backend/internal/resource/application/service.go
git commit -m "feat(p3): add case run lifecycle, evidence and wait tools"
```

### Task 8: Agent 集成（P3 profiles/任务上下文/工具/worker sink）

**Files:**
- Create: `backend/internal/agent/application/p3_task_context.go`
- Create: `backend/internal/agent/application/p3_tools.go`
- Create: `backend/internal/agent/application/p3_profiles.go`（或 extend `profiles.go`）
- Modify: `backend/internal/agent/application/runner.go`（RunnerDeps/Prepare/taskContext/userMessage/writeOutputs）
- Modify: `backend/internal/agent/application/ports_agent.go`（WorkItemPayload/ProfileKeyForWorkType/WorkType 常量）
- Modify: `backend/internal/agent/application/tools/registry.go`（Scope/Deps/NewP3Registry）
- Modify: `backend/internal/agent/application/context.go`（渲染 ExecutionTask）
- Modify: `backend/internal/agent/application/worker.go`（RunSink option）
- Tests: `p3_task_context_test.go`、`p3_tools_test.go`、`runner_p3_test.go`、`worker_sink_test.go`、`context_test.go`

**Interfaces:**
- `type ExecutionTask struct { CaseRunID, WaveID, IntentID, CaseVersionID int64; CaseTitle, Description, Preconditions, HumanResponse, UserMessage string; VersionSUTs []ExecutionSUT; Steps []ExecutionStep; Assertions []ExecutionAssertion; PriorSteps []ExecutionPriorStep; PriorAssertions []ExecutionPriorAssertion }`
- `type ExecutionPriorStep struct { Seq int; Status, Observed, Error string; TargetVersionSUTID int64 }`；`type ExecutionPriorAssertion struct { StepSeq, AssertionSeq int; Kind, Verdict, Expected, Actual, Reason string; TargetVersionSUTID int64 }`
- `type ExecutionSUT struct { VersionSUTID int64; SUTName, EnvironmentName, BaseURL string; PageURLs, APIEndpoints []string; AccountRoles []string }`
- `type ExecutionStep struct { Seq int; Action, Expected, AccountRole string; TargetVersionSUTID int64; PageID, APIID *int64 }`
- `type ExecutionAssertion struct { StepSeq, AssertionSeq int; Kind, Expected string; TargetVersionSUTID int64 }`
- `type TaskContextProvider interface { ExecutionTask(ctx context.Context, item agent.WorkItem) (ExecutionTask, error) }`
- `tools.Scope` 增加：`WaveID, CaseRunID, IntentID, DispatchEpoch int64; DispatchToken string`
- `tools.Deps` 增加：`Execution ExecutionPort`
- `type ExecutionPort interface { StepRecord(ctx, Scope, StepRecordInput) (any,error); AssertionRecord(ctx, Scope, AssertionRecordInput) (any,error); EvidenceText(ctx, Scope, EvidenceTextInput) (any,error); CaseFinish(ctx, Scope, CaseFinishInput) (any,error); Wait(ctx, Scope, WaitInput) (any,error); HumanRequest(ctx, Scope, HumanRequestInput) (any,error); Browser(ctx, Scope, action string, args json.RawMessage) (any,error); HTTP(ctx, Scope, json.RawMessage) (any,error); DBQuery(ctx, Scope, json.RawMessage) (any,error); SchemaRead(ctx, Scope, json.RawMessage) (any,error); AccountCredentials(ctx, Scope, AccountCredentialsInput) (any,error); ResultRead(ctx, Scope, ResultReadInput) (any,error); EvidenceRead(ctx, Scope, EvidenceReadInput) (any,error); CoverageRead(ctx, Scope) (any,error); FailureSubmit(ctx, Scope, FailureSubmitInput) (any,error); CoverageSubmit(ctx, Scope, CoverageSubmitInput) (any,error) }`
- `type RunSink interface { OnRunFinished(ctx context.Context, item agent.WorkItem, result RunResult, runErr error) error }`；`func WithWorkerRunSink(sink RunSink) WorkerOption`
- 新常量：`WorkTypeExecuteCase="execute.case"`、`WorkTypeAnalyzeFailure="analyze.failure"`、`WorkTypeAnalyzeCoverage="analyze.coverage"`；`ProfileKeyCaseExecutor="case_executor"`、`ProfileKeyFailureAnalyst="failure_analyst"`、`ProfileKeyCoverageAnalyst="coverage_analyst"`。
- `ProfileKeyForWorkType` 映射三类型；`WorkItemPayload` 增加 `IntentID/WaveID/CaseRunID/DispatchEpoch/DispatchToken`（payload 解析十进制字符串）。
- `RunnerDeps` 增加 `TaskProvider TaskContextProvider`；P3 类型无 provider → `RunError{Reason:"tool_failure",Retryable:false}`。

- [ ] **Step 1: 写失败的测试**

- `TestParseP3WorkItemPayload`：payload `{intent_id,wave_id,case_run_id,dispatch_epoch,dispatch_token}` 解析正确；非法 ID 报错。
- `TestProfileKeyForWorkTypeP3`：三类型映射；`case.confirm` 仍 false。
- `TestP3ProfilesToolsAndBudgets`：3 profiles 存在；`case_executor` 包含 `execution.wait`/`browser.*`/`human.request`；failure/coverage 不含浏览器工具；`[profile:<key>]` 在 prompt。
- `TestBuildTaskBlockIncludesExecution`：ExecutionTask 渲染步骤/断言/已完成结果。
- `TestRunnerUsesExecutionTaskProvider`：fake provider 返回 task；context packet 含 case title/step；无 provider 报 tool_failure。
- `TestWorkerCallsRunSink`：成功/失败各调用一次；lease lost 不调用。
- `TestP3ToolsScopeDoesNotExposeVersionID`：所有新工具 schema 不含 `version_id/versionId`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/application/... -run 'TestParseP3|TestProfileKeyForWorkTypeP3|TestP3Profiles|TestBuildTaskBlockIncludesExecution|TestRunnerUsesExecutionTaskProvider|TestWorkerCallsRunSink|TestP3ToolsScope' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 P3 profiles/任务上下文/工具/sink**

按 Spec §7。要点：
- `P3Profiles()` 返回 3 profile；`EnsureProfiles` seed 幂等（不覆盖管理员修改），main 处合并 P1/P2/P3。
- `case_executor` prompt 含逐步执行、step/assertion 记录、`execution.wait`、`human.request`、不虚构 passed；budgets `{60,780,900}`。
- `TaskContextProvider` 适配器：`execution/infrastructure/adapters/task_provider.go` 实现 `agentapp.TaskContextProvider`，调用 `CaseRunner.Prepare`，把 `application.TaskSnapshot` 映射为 `agentapp.ExecutionTask`（不跨上下文引用 domain 类型）。
- `NewP3Registry(deps)` = P2 tools + P3 tools；runner 用 P3 registry（P1/P2 profile 不注册 P3 工具也不受影响，因为 Definitions 按 allowed_tools 过滤）。
- `tools.Scope` 扩字段；runner `executeTools` 用 payload 填。
- `worker` 在 `runErr`/`result` 后调用 sink（lease lost 跳过）；sink error 只记录日志，不阻断 work item 完成/失败。
- `writeOutputs` 对 3 profile 返回 nil；`userMessage` 用 `Task.Execution.UserMessage`。

- [ ] **Step 4: 跑测试通过 + executor 回归**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/agent/... -v`
Expected: PASS（含 P1/P2 全部）。
Run: `cd executor; pnpm test`
Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/agent
git commit -m "feat(p3): add execution profiles, task context, tools and run sink"
```

---

### Task 9: 暂停/恢复 + 登录接管 human_request + 恢复/离线/租约/退出执行集

**Files:**
- Create: `backend/internal/execution/application/{human.go,pause.go,recovery.go}`
- Create: `backend/internal/execution/application/{human_test.go,pause_test.go,recovery_test.go}`
- Modify: `backend/internal/execution/application/{case_run.go,scheduler.go,registry.go}`（RunCanceller/active runs）
- Create: `backend/internal/execution/infrastructure/adapters/{team_room.go,work_item_gap.go}`

**Interfaces:**
- `type HumanService struct { ... }`：`Create(ctx, scope tools.Scope, in HumanCreateInput) (domain.HumanRequest, error)`；`Resolve(ctx, id, actorID int64, response map[string]any) (domain.HumanRequest, error)`；`Cancel(ctx,id,actorID int64) error`；`Expire(ctx, now time.Time) (int,error)`。
- `type PauseService struct { ... }`：`PauseWave(ctx, versionID, waveID, actorID int64, mode, reason string) (domain.RunWave,error)`；`ResumeWave`；`PauseCaseRun`；`ResumeCaseRun`；`CancelWave`；`CancelIntent`。
- `type Recovery struct { ... }`：`Reconcile(ctx) error`：执行器 offline/lease 过期/human 过期/context TTL/stale-superseded/孤儿 context。
- `type RunCanceller interface { RegisterRun(caseRunID int64, cancel context.CancelFunc); CancelRun(caseRunID int64); ClearRun(caseRunID int64) }`（CaseRunner 实现）。
- 错误码：`state_conflict`（已有 open human 时 resume）、`not_found`、`invalid_argument`。

- [ ] **Step 1: 写失败的测试**

- `TestPauseWaveGracefulAndImmediate`：graceful 不取消 running、停止 pending；immediate 取消 running→paused；重复 pause 幂等；paused 仍占 active。
- `TestResumeWaveRejectsOpenHuman`：open human → 409；resolve 后可 resume。
- `TestHumanResolveResumesCase`：create → case/wave paused/work item waiting；resolve code → case pending/epoch+1/new work item；不泄露 code。
- `TestHumanTimeoutFailsCase`：expire → human expired、case failed(session)、wave 继续。
- `TestPauseDuringWaitCancels`：FakePort 的 Wait 被 ctx 取消 → case paused（不是 failed）。
- `TestExecutorOfflineRedispatch`：offline + attempts<3 → interrupted→pending、epoch+1、旧 token 写入 false；attempts=3 → failed(environment)。
- `TestLeaseExpiredRecovery`：lease_until 过期 → interrupted→pending；API 重启不批量中断。
- `TestSupersededCancelsActive`：case version stale → intent/case_run superseded、work item done、无新 intent。
- `TestCaseRunPauseResume`：pending→paused→pending；running immediate→paused；resume 从首个未通过 step。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'TestPause|TestResume|TestHuman|TestExecutorOffline|TestLeaseExpired|TestSuperseded|TestCaseRunPause' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 human/pause/recovery**

按 Spec §4.7/§5.5/§5.7/§5.8/§4.9/§6.8/§6.9。要点：
- `Create`：校验 kind=login_takeover、case_run scope、无 open human（同 case 最多 3 次）；插入 open；case_run paused(reason=waiting_human)、wave paused(pause_mode=graceful,reason=waiting_human)；取消 agent run（RunCanceller）；work item 置 waiting_human。**新增** `agentapp.WorkQueue.MarkWaitingHuman(ctx, id, epoch int64) error` 与 `agentpg.WorkItemRepository` 实现（`UPDATE work_item SET status='waiting_human' WHERE id=$1 AND lease_epoch=$2 AND status='running'`），并加入 `WorkerQueue` 接口；发事件+帖。
- `Resolve`：CAS open→resolved；旧 work item done；case_run pending/epoch+1；wave running（若 pause_reason=waiting_human 且无其他 open）；发事件；下次 tick 派发。
- `Expire`：批量 open 过期 → expired；case_run failed(session)；wave 继续；发事件。
- `PauseWave`：graceful/immediate；immediate 调 CancelRun + executor cancel；写 pause_mode/reason；发事件；更新 counts。
- `ResumeWave`：open human → 409；paused→running；paused case→pending/epoch+1；发事件。
- `Recovery.Reconcile`：先 executor offline/lease；再 human expire；再 stale；再 context TTL/孤儿；每步幂等；不批量中断 running（只处理过期 lease/明确 offline/非 current）。

- [ ] **Step 4: 跑测试通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution
git commit -m "feat(p3): add pause, resume, human takeover and recovery"
```

---

### Task 10: 失败/覆盖分析（确定性兜底 + Agent 增强 + design.gap）

**Files:**
- Create: `backend/internal/execution/application/{analysis.go,coverage.go}`
- Create: `backend/internal/execution/application/{analysis_test.go,coverage_test.go}`
- Modify: `backend/internal/execution/application/{case_run.go,scheduler.go}`（wave 终态触发）
- Create: `backend/internal/execution/infrastructure/adapters/{work_item_gap.go,team_room.go}`（若 Task 9 已建则扩展）

**Interfaces:**
- `type AnalysisService struct { ... }`：`AnalyzeWave(ctx, waveID int64) error`（确定性：failure_class/summary/问题候选/覆盖缺口）；`EnsureTasks(ctx, waveID int64) error`（ensure analyze.failure/coverage work items）。
- `type CoverageAnalyst` 的提交入口在 CaseRunner/工具：`SubmitFailure(ctx, scope, in FailureSubmitInput) (any,error)`；`SubmitCoverage(ctx, scope, in CoverageSubmitInput) (any,error)`。
- `type GapEnsurer interface { EnsureGap(ctx, versionID, requirementID, caseVersionID int64, reason string) error }`（适配器调用 P2 design `RequestGap`/WorkQueue ensure，dedupe `gap:{requirement_id}:{case_version_id|requirement_version_id}`）。
- 问题候选 payload：`{case_run_id,case_version_id,failure_class,category,severity,dedupe_hint,summary}`；事件 `execution.failure_candidate`；帖 `team=execution,kind=finding`。

- [ ] **Step 1: 写失败的测试**

- `TestAnalyzeWaveClassifiesFailures`：Assertion failed → application；tool error → automation；无证据 application → inconclusive。
- `TestFailureCandidateOnlyApplication`：非 application 不发 `execution.failure_candidate`。
- `TestCoverageGapCreatesDesignGap`：requirement 有 confirmed case 全 failed → `design.gap` ensure 一次（幂等）；有 passed → 不建；`feature.auto_gap_design=false` → 不 ensure 只事件。
- `TestEnsureAnalysisTasksIdempotent`：wave 终态两次 → 1 个 failure/coverage work item。
- `TestSubmitFailureCannotEscalate`：failure_analyst 把 automation 改 application → 拒绝；application→automation 允许（有证据）。
- `TestSubmitCoverageValidatesWave`：跨 wave case_run → not_found。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -run 'TestAnalyze|TestFailure|TestCoverage|TestSubmit' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 analysis/coverage**

按 Spec §8.1–§8.3；确定性兜底先于 Agent；问题候选 dedupe_hint 算法；coverage 只处理 current+confirmed case 的执行结果；`design.gap` 复用 P2 work item 类型；wave 终态触发 `EnsureTasks`。`SubmitFailure/Coverage` 校验 scope/wave/class 迁移，写 case_run/summary、帖、事件。

- [ ] **Step 4: 跑测试通过**

Run: `cd backend; ..\.tools\go\bin\go.exe test ./internal/execution/application -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add backend/internal/execution
git commit -m "feat(p3): add failure and coverage analysis with design gaps"
```

---

### Task 11: HTTP API + server/main 装配 + 集成测试

**Files:**
- Create: `backend/internal/execution/interfaces/httpapi/{handler.go,routes.go,dto.go,views.go}`
- Create: `backend/internal/execution/interfaces/httpapi/handler_test.go`
- Modify: `backend/internal/server/server.go`（Deps/route registration）
- Modify: `backend/internal/server/server_integration_test.go`（P3 routes）
- Modify: `backend/cmd/eztest/main.go`（全量装配/后台 goroutine/优雅停止）
- Modify: `backend/internal/agent/application/worker.go`（若 sink option 未完成，Task 8 已做）

**Interfaces:**
- `type Handler struct { ... }` 构造 `NewHandler(deps HandlerDeps)`；`func (h *Handler) Routes(auth, admin func(http.Handler) http.Handler) *http.ServeMux` 或在 server 内注册。
- `server.Deps` 增加 `Execution *executionhttp.Handler`；`server.New` 增加 `registerExecutionRoutes`。
- main 装配顺序：settings → ids → clock → audit/eventing → identity/llm/storage/resource/version → design → execution repos/adapters/services → P3 profiles/tools/provider → runner/worker(sink) → scheduler/poller/recovery goroutines → server。
- 22 paths 按 Spec §9.2；认证/错误码/分页/ID 字符串。

- [ ] **Step 1: 写失败的 server 集成测试**

`TestP3ExecutionRoutes`：未登录 401；`GET overview` 200（空态）；`GET waves` 200 `{data:[],next_cursor:null,has_more:false}`；`POST pause` 404；`GET evidence/1/content` 404；`GET /api/admin/executors` 普通用户 403、admin 200；`POST /api/human-requests/1/resolve` 404。
`TestP3ExecutionEndToEnd`（可延后到验收，但计划要求）：建资源/版本/需求/用例（SQL 或 P2 service）→ intent → wave（fake executor/agent）→ passed → evidence 下载 sha 一致。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/server -run TestP3ExecutionRoutes -v`
Expected: FAIL（路由未注册）。

- [ ] **Step 3: 实现 handler/routes/DTO + server 装配**

DTO 映射：ID `strconv.FormatInt`；列表 envelope；pause/resume request；resolve response；admin executors。错误：`platform.Error` → `httpx.Error`；`lease_lost`/`state_conflict`/`not_found`/`invalid_argument`。证据下载：`http.ServeContent`/`io.Copy` + `Content-Disposition` + sha 校验；不得返回路径。

- [ ] **Step 4: main 装配 + 后台循环**

`executionScheduler`（1s）：`Scheduler.Reconcile` + `Recovery.Reconcile`；`executorPoller`（heartbeat 秒）：`Registry.PollOnce`；失败日志不退出。`worker` 增加 sink：`caseRunner.Finalize`。所有 goroutine 用 `ctx` 与 `WaitGroup` 优雅停止（沿用 main 模式）。P3 profiles seed + P3 registry + task provider + execution tools 注入。

- [ ] **Step 5: 跑 server/集成测试 + 前端 API 契约解析**

Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/server -run 'TestP3|TestP0|TestP2' -v`
Expected: PASS。
Run: `node scripts/p3-contract-check.cjs --draft`
Expected: PASS。

- [ ] **Step 6: 提交**

```powershell
git add backend/internal/execution/interfaces backend/internal/server backend/cmd/eztest/main.go
git commit -m "feat(p3): expose execution API and wire runtime"
```

---

### Task 12: 前端执行 Tab（三栏/实时/结果/证据/暂停/接管/深链）

**Files:**
- Create: `frontend/src/features/execution/{types.ts,api.ts,labels.ts,test-fixtures.ts,ExecutionTab.tsx,ExecutionToolbar.tsx,WaveList.tsx,LiveTrace.tsx,ResultsPanel.tsx,CaseRunDetail.tsx,EvidenceViewer.tsx,HumanRequestBanner.tsx,PausedBanner.tsx,PauseDialog.tsx}`
- Create tests: `api.test.ts`、`ExecutionTab.test.tsx`、`PauseDialog.test.tsx`、`ResultsPanel.test.tsx`、`EvidenceViewer.test.tsx`、`HumanRequestBanner.test.tsx`、`labels.test.ts`
- Modify: `frontend/src/features/versions/VersionDetailPage.tsx`（execution branch/`?wave=`/`?case_run=`）
- Modify: `frontend/src/features/versions/VersionDetailPage.test.tsx`
- Modify: `frontend/src/features/clarification/useVersionEvents.ts`（execution handlers）
- Modify: `frontend/src/lib/format.ts` + `format.test.ts`（`formatDuration`）
- Create: `frontend/e2e/p3.spec.ts`（真实 LLM，Task 14 运行）

**Interfaces:**
- `api.ts`：`getOverview(versionId)`、`listWaves(versionId,{before,limit})`、`getWave`、`pauseWave(versionId,waveId,{mode,reason})`、`resumeWave`、`cancelWave`、`listCaseRuns(versionId,waveId,{before,limit,status})`、`getCaseRun(versionId,caseRunId)`、`pauseCaseRun`、`resumeCaseRun`、`listEvidence(versionId,{caseRunId,versionSutId,kind,before,limit})`、`getEvidence`、`downloadEvidence`、`listHumanRequests(versionId,{status})`、`resolveHumanRequest(id,response)`、`cancelHumanRequest`、`createIntent(versionId,{caseVersionId,purpose})`、`listIntents`。
- `types.ts`：`ExecutionOverview/RunWave/CaseRun/CaseRunDetail/Evidence/HumanRequest/Intent/ExecutorStatus`（字段与 Spec §9.3 一致；ID 字符串）。
- `labels.ts`：`CASE_RUN_STATUS_LABEL/TONE`、`FAILURE_CLASS_LABEL`、`WAVE_STATUS_LABEL/TONE`、`HUMAN_REQUEST_STATUS_LABEL`、`PAUSE_MODE_LABEL`。
- `useVersionEvents` 扩展（不新增 hook）：增加可选 `onWaveChanged/onCaseRunChanged/onEvidenceCreated/onHumanRequestChanged/onExecutionChanged` 回调；订阅 `version:{id}`，路由 `wave.*/case_run.*/evidence.created/human_request.*/execution.*`；未知忽略；ExecutionTab 去抖 300ms 重拉快照。
- `formatDuration(ms: number): string`（`8.4s`/`1m 02s`）。

- [ ] **Step 1: 写失败的单测**

- `api.test.ts`：URL/method/`Idempotency-Key`/错误信封→ApiError；`downloadEvidence` 走 apiDownload。
- `labels.test.ts`：四态/9 failure class/状态 tone。
- `PauseDialog.test.tsx`：默认 graceful；immediate 二次确认；提交中禁用；`already_paused` 幂等文案。
- `ResultsPanel.test.tsx`：failed/blocked 置顶；四态徽章；展开步骤/断言；重跑调用 createIntent。
- `EvidenceViewer.test.tsx`：截图 objectURL revoke；文本；下载失败 ErrorState；显示 sha/size。
- `HumanRequestBanner.test.tsx`：渲染原因/角色/到期；resolve 提交 code；成功后消失。
- `ExecutionTab.test.tsx`：加载/空态/错误重试/`?wave=` 深链/SSE 去抖刷新/暂停横幅。
- `VersionDetailPage.test.tsx`：execution 不再占位；`?wave=`/`?case_run=` 透传。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend; pnpm test -- src/features/execution`
Expected: FAIL（文件不存在）。

- [ ] **Step 3: 实现 types/api/labels/hooks/组件**

按 Spec §12；三栏布局用 Tailwind（不照搬 mock hex）；顶部 toolbar + PausedBanner + HumanRequestBanner；左 WaveList/中 LiveTrace/右 ResultsPanel；窄屏 stack；空态/错误；不新增全局路由；不改 VersionHeader。

- [ ] **Step 4: 接线 VersionDetailPage/useVersionEvents/format**

`VersionDetailPage`：execution 分支渲染 `<ExecutionTab versionId ... />`；`handleTabChange` 保留 `?wave=`/`?case_run=`；`searchParams` 读取并传入。`useVersionEvents` 增加执行事件转发（不破坏现有分支）。`formatDuration`。

- [ ] **Step 5: 跑前端门禁**

Run: `cd frontend; pnpm lint; pnpm typecheck; pnpm test`
Expected: PASS；execution 测试全绿；既有 54 文件不回归。

- [ ] **Step 6: 提交**

```powershell
git add frontend/src frontend/e2e/p3.spec.ts
git commit -m "feat(p3): add execution workspace UI"
```

---

### Task 13: 双 SUT demo fixture + 目标 DB 夹具

**Files:**
- Modify: `scripts/fixture-target.cjs`（`/p3/*`、`FIXTURE_VARIANT=orders|payments`）
- Create: `scripts/p3-fixture-db.ps1`
- Create: `scripts/fixture-p3.test.cjs`（node:test + fetch；随机端口启动 fixture）
- Modify: `scripts/dev-executor.ps1`（从 DB 读 `execution.executor_token`）
- Modify: `executor/README.md`（P3 token/env 说明，如适用）

**Interfaces:**
- 路由：`/p3/login`（GET/POST；`?mfa=1` 两阶段，code `246810`）、`/p3/home`、`/p3/orders`、`/p3/orders/{id}`、`/p3/refund`（POST 创建退款）、`/p3/slow?ms=8000`、`/p3/timeout-window?ms=10000`（`#confirm-dialog` 倒计时后移除，随后 `#window-expired`）、`/api/p3/orders`、`/api/p3/payments`。
- DB 夹具：`p3_orders`/`p3_refunds` 表 + seed；连接串从 `EZTEST_TEST_DATABASE_URL`/命令行参数派生；只用于验收库。

- [ ] **Step 1: 写失败的 fixture 测试**

`scripts/fixture-p3.test.cjs`：启动 fixture（orders/payments 两个实例）→ 断言 `/p3/login` 200、错误密码 401、2FA 阶段 200 且含 `#mfa`、正确 code 登录、`/p3/orders/1` 含 `¥5,200.00`、`/p3/refund` 创建后 payments 可见、`/p3/timeout-window?ms=200` 200、未知路由 404。

Run: `node --test scripts/fixture-p3.test.cjs`
Expected: FAIL（路由不存在）。

- [ ] **Step 2: 实现 fixture 扩展**

保留现有路由；新增 P3 路由；按 `FIXTURE_VARIANT` 决定 2FA/payments 差异；不引入依赖；loopback；请求体限制；错误处理沿用。

- [ ] **Step 3: 写 DB 夹具 + dev-executor token**

`p3-fixture-db.ps1`：psql 建表/清表/seed；幂等；只允许 `_e2e/_test` 库。`dev-executor.ps1`：查询 `system_setting` 的 `execution.executor_token`（psql），设置 `EZTEST_EXECUTOR_TOKEN`；空值提示。

- [ ] **Step 4: 跑 fixture 测试 + DB 冒烟**

Run: `node --test scripts/fixture-p3.test.cjs` → PASS。
Run: `powershell.exe -File scripts/p3-fixture-db.ps1 -DatabaseUrl $env:EZTEST_TEST_DATABASE_URL` → 表/seed 存在（psql 查询）→ PASS。

- [ ] **Step 5: 提交**

```powershell
git add scripts/fixture-target.cjs scripts/fixture-p3.test.cjs scripts/p3-fixture-db.ps1 scripts/dev-executor.ps1 executor/README.md
git commit -m "test(p3): add two-SUT demo fixture and target database"
```

---

### Task 14: 验收脚本（真实 LLM）+ 浏览器 E2E + 回归适配

**Files:**
- Create: `scripts/p3-acceptance.ps1`（真实 LLM API 验收；`-LongWait`）
- Create: `scripts/p3-e2e.ps1`
- Modify/Create: `frontend/e2e/p3.spec.ts`（Task 12 创建，本任务完善/运行）
- Modify: `scripts/p2-contract-check.cjs`（兼容模式：P2 增量存在，不断言根 frozen P2）
- Modify: `scripts/p1-acceptance.ps1`/`scripts/p2-acceptance.ps1`/`p2-e2e.ps1` 的 `0.2.0-p2` 断言 → `0.2.0-p3`
- Modify: `scripts/test.ps1`（如需纳入 executor 测试？保持现状，计划不改）
- Create: `.tmp/p3-acceptance/` 证据（运行产生）

**Interfaces:**
- `p3-acceptance.ps1` 参数：`-LongWait`、`-KeepRunning`；环境：`.env` 4 env + `llm_*` + E2E DB URL；端口 14333/16868/fixture 13335+13337/executor 13336；隔离库 `eztest_v02_e2e`。
- `p3-e2e.ps1`：启动 API/Web/executor/fixtures；Playwright `p3.spec.ts`；截图 `.tmp/p3-acceptance/screenshots/`。

- [ ] **Step 1: 写验收脚本骨架（先失败）**

脚本执行：清理/重建 e2e 库 → 启动 API（迁移 20）→ admin login → 写 LLM 配置（真实）→ 建 2 SUT/环境/页面/API/DB/账号 → 建版本/需求 → SQL 预置 5 条 draft 用例（单 SUT 通过、跨系统、application 失败、2FA、时间窗口）→ P2 确认 API 确认 → 断言 intent/wave；运行 `-LongWait` 分支。先只实现到“断言 intent 存在”，运行预期失败（API 未部署/实现未完成）。

- [ ] **Step 2: 跑脚本确认失败点清晰**

Run: `powershell.exe -File scripts/p3-acceptance.ps1`
Expected: 失败并输出具体缺失步骤（不是脚本语法错误）。

- [ ] **Step 3: 实现完整验收步骤 1–16（Spec §13.2）**

按 Spec 场景：单 SUT/跨系统/失败/覆盖/接管/暂停/offline/stale/重跑/SSE/时间窗口/回归；所有执行走真实 DeepSeek；断言 `version_sut_id`/sha/证据/事件/无 issue/report 表；`-LongWait` 用 120s。失败时写 `.tmp/p3-acceptance/failure-step.txt`，不伪造通过。

- [ ] **Step 4: 实现浏览器 E2E**

`p3.spec.ts`：API 准备 1 条简单用例（真实 LLM）→ 登录 → `?tab=execution` → 查看波次/结果 → 证据查看器 → 暂停/恢复 → 重跑 → `pageErrors=[]` → 截图。`p3-e2e.ps1` 启动服务并运行。

- [ ] **Step 5: 回归适配**

`p2-contract-check.cjs` 改为校验 P2 必需 paths/schemas 存在（不校验根 x-frozen），并在 P3 冻结前跑通；P1/P2 验收脚本版本断言改 `0.2.0-p3`。跑：
`scripts/p1-acceptance.ps1 -FakeLLMPort 13334`、`scripts/p2-acceptance.ps1 -FakeLLMPort 13334`、`scripts/p2-e2e.ps1`。
Expected: PASS（fake 回归，不属于 P3 验收）。

- [ ] **Step 6: 跑 P3 真实验收（无人值守）**

Run: `powershell.exe -File scripts/p3-acceptance.ps1`
Expected: 全场景 PASS；证据 `.tmp/p3-acceptance/api.log`、`fixture-*.log`、`executor.log`。
Run: `powershell.exe -File scripts/p3-acceptance.ps1 -LongWait`
Expected: 120s 时间窗口场景 PASS。
Run: `powershell.exe -File scripts/p3-e2e.ps1`
Expected: 1 passed；截图落盘。
若真实 LLM 失败：重跑 ≤2 次；仍失败 → 记录并 handoff `blocked`，不得用 fake 替代。

- [ ] **Step 7: 提交**

```powershell
git add scripts frontend/e2e/p3.spec.ts
git commit -m "test(p3): add real-LLM acceptance, E2E and demo scenarios"
```

---

### Task 15: 契约冻结 + 全量门禁 + `phase-03-handoff.md`

**Files:**
- Modify: `docs/version-V0.2/contracts/{openapi.yaml,events.md,db-schema.md,migrations-order.md,task-types.md,agent-profiles-tools.md,settings.md,executor-protocol.md,README.md}`
- Modify: `scripts/p3-contract-check.cjs`（frozen 模式）
- Create: `docs/version-V0.2/phases/phase-03-handoff.md`（按模板）
- Modify: `backend/cmd/eztest/main.go`（version `0.2.0-p3` 若未改）
- Modify: 任何验收脚本版本断言
- Evidence: `.tmp/p3/final-gate.log` 等

**Interfaces:**
- `p3-contract-check.cjs`：断言 `x-frozen: P3`、无 `x-draft`、22 paths/必需 schemas、20 事件、10 表、3 迁移、3 tasks、3 profiles、工具、49 settings、executor-protocol 存在；输出计数。
- Handoff：Spec §0/§17/§18 + 必含 handoff 三项（表/API/协议/intent 唯一键/并发 3/2/1、P4 输入、风险）。

- [ ] **Step 1: 冻结标记与版本**

`openapi.yaml`：`info.version: 0.2.0-p3`、`x-frozen: P3`、删 `x-draft`；events/db-schema/migrations-order/task-types/agent-profiles-tools/settings/executor-protocol 节标记 `frozen: P3`；README 追加 P3 状态与计数；`main.go` version `0.2.0-p3`；`/api/health` 同步。

- [ ] **Step 2: 跑契约冻结检查**

Run: `node scripts/p3-contract-check.cjs`
Expected: PASS；输出 paths/ops/schemas/events/tables/migrations/profiles/tools/keys 计数。

- [ ] **Step 3: 全量门禁**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1 2>&1 | Tee-Object .tmp/p3/final-gate.log`
Expected: EXIT=0。
Run: `cd backend; ..\.tools\go\bin\go.exe test -tags=integration ./internal/db/...` → 20 迁移、重复执行 applied 为空。
Run: `cd executor; pnpm test` → PASS。
Run: P1/P2 回归（Task 14 命令）→ PASS。

- [ ] **Step 4: 写 `phase-03-handoff.md`**

按 `contracts/handoff-template.md`：状态 completed/blocked；日期 2026-11-01；阶段 P3；实施分支 `feat/v0.2-p3`；spec/plan 链接；§1 完成范围（对照 Spec §1/§13.2）；§2 契约变更表 + 迁移 3 个 + 无破坏性；§3 证据（test.ps1、p3-acceptance、-LongWait、p3-e2e、P1/P2 回归、contract-check、迁移幂等；日志/截图路径）；§4 已知缺口/偏差（真实 LLM 波动、SQL 预置验收、登录接管无远程可视化、证据上限、P2 遗留、`analysis_run` 归 P4、executor push 留 P7 等）；§5 P4 输入（表/事件/查询/建议）；§6 风险。必含执行表/API/executor 协议/intent 唯一键/并发 3/2/1。

- [ ] **Step 5: 最终 diff 自检 + 提交**

Run: `git status --short`（只应包含预期文件；dirty 用户文件不纳入）；`git diff --stat b4123c8..HEAD`。
Expected: P3 范围文件；不修改设计文档（除 contracts/Agent.md 标注）。

```powershell
git add docs/version-V0.2/contracts docs/version-V0.2/phases/phase-03-handoff.md backend/cmd/eztest/main.go scripts
git commit -m "docs(p3): freeze contracts and add phase-03 handoff"
```

- [ ] **Step 6: 停止**

向用户报告：已完成/PASS 证据/缺口/风险；**不自动进入 P4**。

---

## 并行执行映射（Team Lead 使用）

| 子代理 | 任务 | 写作用域 | 依赖 |
|---|---|---|---|
| Lead | T0/T1/T2/T15 + 评审/门禁 | `Agent.md`、`contracts/**`、`migrations/**`、handoff | 全部 |
| Backend-core | T3/T4/T6/T9/T10 | `internal/execution/{domain,application,infrastructure/postgres}` | T1/T2 |
| Backend-agent | T5/T7/T8 | `executor/**`、`agent/**`、`execution/{adapters,executor}` | T1/T2 |
| Backend-api | T11 | `execution/interfaces/httpapi`、`server`、`main` | T4–T10 |
| Frontend | T12 | `frontend/src/features/execution/**`、version/SSE/format | T2（契约） |
| QA/fixture | T13/T14 | `scripts/**`、`frontend/e2e/p3.spec.ts` | T13 可并行；T14 依赖 T11/T12 |

**并行规则**：T1/T2 先完成；T3 后 T4 与 T6 可并行；T5 与 T6 可并行；T7 依赖 T4/T6；T8 依赖 T7 接口（可先写 fake）；T9/T10 依赖 T7；T12 依赖 T2；T13 依赖 T2（fixture 无后端）；T14 依赖 T11/T12/T13；T15 最后。共享 `contracts`/`openapi`/`migrations-order` 由 Lead 串行写。

## Self-Review（写完执行）

**Spec coverage**：§3 表/迁移→T1；§9 API/§10 事件→T2/T11；§4 状态机→T3/T7；§5 调度→T6；§6 executor/多 SUT/DB/HTTP/等待→T5/T7/T8；§7 profile/tools→T8；§8 分析→T10；§12 前端→T12；§13 fixture/验收→T13/T14；§14 冻结→T15；§17 裁决→贯穿；§18 风险→T15 handoff。无遗漏。

**Placeholder scan**：无 TBD/TODO；所有路径/签名/枚举/默认值已写死。

**Type consistency**：`domain.ExecutionIntent/RunWave/CaseRun/StepResult/AssertionResult/CaseSnapshot/Quota` 在 T3 定义、T4/T6/T7/T9/T10 使用；`tools.Scope` 在 T8 扩展、T7 使用；`ExecutionPort` 在 T8 定义、execution app 实现；`RunSink` 在 T8；`CaseReader/VersionReader` 在 T6 定义、T7/T11 适配。命名一致。

**Review Focus**：6 条均在 T3/T6/T7/T8/T9/T11/T14 有对应测试。

**Proportion**：计划约 700–900 行，spec 约 900 行；以签名/测试名/命令为主，未转录实现体。

