# EzTest P7 全自主闭环与交付 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 P6 执行引擎之上交付 P7：AI 补全、失败分析、问题/报告、只读 DB 断言、`continuing` 增量批次与前端交付，并通过真实 LLM/真实 DB/真实浏览器人工验收。

**Architecture:** 新增 `delivery` 限界上下文（issue/report/analysis_run）承载交付物；`resource` 加环境级 `db_target`；`version` 加 `continue_phase` 与需求创建触发 continuing；`execution` 加批次 kind/source/graph_hash、自主编排 worker、db_assert 与 DB 判定；`mindmap` 加补全写路径/补全 run；`codeintel` 加增量 base 与 issue 锚点。P6 API/事件/报告端点只做加法兼容。

**Tech Stack:** Go 1.2x + pgx/PostgreSQL；Next.js + React + Vitest；Playwright executor；PowerShell 验收脚本；fake LLM。

**Spec:** `docs/superpowers/specs/2026-10-28-eztest-p7-autonomous-loop-design.md`（下称 spec；实现与验收以 spec 为准）

## Global Constraints

- 分支 `feat/p7-autonomous-loop`；**实现者不得执行 `git commit`/`git push`**；Lead 按任务写作用域 stage/commit，验收后本地 ff 合回 `dev`，不 push。
- 严格按 spec；**不实现** P8 项：`version_change` 表、证据 GC、多实例 leader、PDF、外部工单、知识沉淀/评测、视觉断言、Mock/夹具工具、多版本并行。
- 主键雪花 `BIGINT`，应用层生成；JSON/URL/事件/工具参数中 ID 一律十进制字符串；禁止新增自增主键（D29）。
- 迁移只追加，命名 `<context>_<NNNN>_<desc>.sql`，`order.txt` 顺序追加；已应用迁移禁止修改（D34/Agent.md §6.3）。
- 表名/字段/索引遵循 D34；新表审计列 `created_at/updated_at`（引用表只 `created_at`）。
- 文件/二进制不入库：报告走 `EZTEST_REPORT_DIR`，DB 结果走 P6 证据根；只存相对路径 + sha256 + size；0600/0700、原子 rename、根内校验（D30/Agent.md §6.8）。
- 导入方向：domain 不依赖 application/infrastructure/interfaces；application 不依赖 infrastructure/interfaces；跨上下文只走对方 application 接口/事件（Agent.md §6.2）。
- Agent 安全：凭据、storageState、Authorization/Cookie、LLM prompt/原文、证据正文、SQL 结果集不进事件/日志/审计/报告/工具参数；DB 密码明文（D48）但永不出 DTO。
- 真实 LLM 验收 gated `EZTEST_RUN_REAL_LLM=1`；真实 DB 用本机 PG；真实人工验收由 Lead 代用户执行并留档。
- 并行任务只写各自 Files 声明的文件；同波次任务不得修改同一文件；需要共享文件时改到后续串行任务。
- 每个任务先写失败测试，再实现，再跑覆盖测试；Go 单元测试同包，集成测试放 `internal/<ctx>/integration`；前端测试与实现同 feature。
- 命令统一从仓库根执行；Go 用 `..\.tools\go\bin\go.exe`（或 `scripts/test.ps1` 内的解析），前端用 pnpm。

## Review Focus（最容易被忽略、必须有测试钉住的 5 类输入）

1. **冻结基线只读**：补全只能追加节点/改状态；对已冻结节点的 `title/steps/assertions/source` 编辑仍 409 `baseline_frozen`；`case_baseline.graph_hash` 与 `baseline_hash` 不被补全改写。测试放在 T9（mindmap）。
2. **批次/版本完成时序**：自主链未收尾（`autonomy_done=false`）不得出现"版本 completed"；单版本单活跃批次；supplement 批必须有 parent 且 parent 已终态；创建 baseline/incremental 批前所有终态批已收尾。测试放在 T7/T15。
3. **DB 只读与凭据**：写 SQL/DDL 必须被拒并映射 `inconclusive(case)`；连接/权限失败 `blocked(environment)`；密码不出现在 DTO/事件/审计/证据/报告/日志/prompt。测试放在 T5/T14/T22。
4. **报告确定性与 stale**：同批次同输入生成相同 `content`/`input_fingerprint`/Markdown；rerun 或 issue 状态变化后 published→stale 且只发一次；P6 派生报告 DTO 不变。测试放在 T3/T22/T24。
5. **AI 失败降级**：LLM 不可用时批次仍执行/完成，报告 `degraded=true`，页面不崩；补全/失败分析/问题整理失败不阻断。测试放在 T4/T15/T21/T24。

---

## Concurrency Map（实现时并行执行的唯一依据）

| Wave | 任务 | 说明 |
|---|---|---|
| **W1** | T1 | Schema/迁移/验收尾部断言；串行地基 |
| **W2** | T2 ∥ T3 ∥ T4 ∥ T5 ∥ T6 ∥ T7 ∥ T8 ∥ T9 ∥ T10 ∥ T11 | 各上下文核心领域/服务/仓储（文件作用域不重叠） |
| **W3** | T12 ∥ T13 ∥ T14 ∥ T15 ∥ T16 | HTTP/Agent/DB 工具/自主 worker（依赖 W2 接口） |
| **W4** | T17 | 组装根 wiring/profile seed/build；串行集成 |
| **W5** | T18 → (T19 ∥ T20 ∥ T21) | 前端 delivery 先行（T19–T21 依赖其导出/类型），其余三个 feature 并行 |
| **W6** | T22 ∥ T24 → T23 | fake LLM ∥ 文档先行；OpenAPI/acceptance 在文档后补 needle |
| **W7** | T25 ∥ T26 | E2E specs / 后端集成测试（前端 e2e vs 后端测试） |
| **W8** | T27 | 全量门禁 + 修复；串行 |
| **W9** | T28 ∥ T29 | 真实 LLM 验收脚本 / 真实 DB 验收脚本 |
| **W10** | T30 → T31 | 真实 LLM 验收运行 → 真实 DB 验收运行（共享环境，串行） |
| **W11** | T32 | 真实浏览器人工验收（Lead 代用户） |
| **W12** | T33 | 最终整支 review + 修复 + 合回 dev |

- 并行实现者不 commit；Lead 在每波次结束后按任务文件作用域逐个 stage/commit（提交信息见各任务）。
- W2 的 10 个任务可同时启动；若某任务实际需要改共享文件（如 `server.go`/`ports.go`），把该改动移到 W3/W4 对应任务，禁止并行改同一文件。
- 同一任务内的文件写入可顺序执行；跨任务并行只读依赖已合入的接口（Interfaces 块）。

## Pre-flight（T0，一次性，不算任务）

- [ ] 确认分支/干净基线：`git switch feat/p7-autonomous-loop`；`git status --short` 为空；HEAD 包含 spec `99193fd`。
- [ ] 记录基线：`git rev-parse HEAD` 写入 `.superpowers/sdd/2026-10-28-eztest-p7-autonomous-loop/base`。
- [ ] 环境：`.env` 有 `EZTEST_DATABASE_URL`/`EZTEST_TEST_DATABASE_URL`/`llm_*`（不打印值）；PostgreSQL `:5433` 可连；执行器 `:13336` 可启；Chromium 可用。
- [ ] 跑一次基线门禁并留日志：`scripts/test.ps1`、`scripts/acceptance.ps1 -StaticOnly`。
- [ ] 建 SDD 工作区与 ledger：`.superpowers/sdd/2026-10-28-eztest-p7-autonomous-loop/progress.md`，首行 `# SDD ledger — plan: docs/superpowers/plans/2026-10-28-eztest-p7-autonomous-loop.md`。

## Shared Interfaces（由 T1–T11 定义；后续任务消费，不得改名）

> 下面是跨任务契约的**最小集合**；实现可增加未列出的内部方法，但不得修改这里的名称/参数/返回语义。

### Go 类型

```go
// delivery/domain
type Issue struct {
    ID, VersionID int64
    BatchID, CaseRunID, CaseNodeID, StepResultID *int64
    Source, Category, Severity, Status, Confidence string
    FailureClass, DedupeKey, Title, Description, ReproSteps, Expected, Actual string
    RerunSuggestion, ExternalRef, DismissedReason string
    RecurrenceCount, Revision int
    CreatedBy, UpdatedBy int64
    ConfirmedBy, DismissedBy *int64
    ConfirmedAt, DismissedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}

type IssueRef struct { IssueID int64; RefKind, RefID string }

type Report struct {
    ID, VersionID, BatchID int64
    Revision int
    Status, Title string
    Content ReportContent
    InputFingerprint string
    MarkdownPath, MarkdownSHA256 string
    MarkdownSize int64
    StaleReason string
    GeneratedBy *int64
    PublishedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}

type AnalysisRun struct {
    ID, VersionID, BatchID int64
    Kind, Status, Model, PromptVersion string
    AgentSessionID, AgentRunID *int64
    Input, Result, Stats, Errors json.RawMessage
    CreatedBy int64
    StartedAt, FinishedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}

type IssueDraft struct {
    DedupeKey, Title, Description, ReproSteps, Expected, Actual string
    Severity, Confidence, Category string
    CaseRunID, CaseNodeID *int64
    EvidenceIDs []int64
    CodeRefs []CodeRef
    RequirementIDs []int64
    MergeIntoIssueID *int64
}

type QueryResult struct {
    Columns []string
    Rows [][]string
    RowCount int
    Truncated bool
    ElapsedMS int64
}
```

### Application 接口（delivery）

```go
type IssueService interface {
    CreateHuman(ctx context.Context, versionID, actor int64, cmd CreateIssueCommand) (domain.Issue, error)
    ApplyDrafts(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error)
    Confirm(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, comment string) (domain.Issue, error)
    Dismiss(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, reason string) (domain.Issue, error)
    Get(ctx context.Context, versionID, issueID int64) (domain.Issue, error)
    List(ctx context.Context, versionID int64, f IssueFilter) ([]domain.Issue, bool, error)
}

type ReportService interface {
    Ensure(ctx context.Context, versionID, batchID, actor int64) (domain.Report, bool, error)
    Regenerate(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error)
    Get(ctx context.Context, versionID, reportID int64) (domain.Report, error)
    List(ctx context.Context, versionID int64, f ReportFilter) ([]domain.Report, bool, error)
    Diff(ctx context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error)
    Export(ctx context.Context, versionID, reportID int64) (domain.ReportExport, error)
}

type AnalysisService interface {
    StartFailure(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error)
    StartTriage(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error)
    Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error)
    List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error)
    ProcessOnce(ctx context.Context) error
    Worker(ctx context.Context)
    Stop()
    RecoverStale(ctx context.Context) (int64, error)
}
```

### Application 接口（version / execution / mindmap / resource / codeintel / agent）

```go
// version
func (s *VersionService) MarkContinuing(ctx context.Context, versionID, actor int64) (Version, error) // completed→continuing CAS
func (s *VersionService) SetContinuePhase(ctx context.Context, versionID int64, phase string) error   // ""|clarifying|designing|executing
func (s *VersionService) TryFinalize(ctx context.Context, versionID int64) (bool, error)              // finalize 条件见 spec §5.1
func (s *VersionService) CompleteContinuing(ctx context.Context, versionID, actor int64) (Version, error)

// execution batch
type CreateBatchCommand struct {
    CaseIDs []int64
    Kind string // baseline|incremental|supplement
    ParentBatchID *int64
}
func (s *BatchService) Create(ctx context.Context, versionID, actor int64, cmd CreateBatchCommand) (*domain.RunBatch, error)

// execution autonomy
type AnalysisPort interface {
    StartFailureAnalysis(ctx context.Context, versionID, batchID, actor int64) (int64, error)
    StartIssueTriage(ctx context.Context, versionID, batchID, actor int64) (int64, error)
    GetAnalysis(ctx context.Context, batchID int64, kind string) (*AnalysisView, error)
    EnsureReport(ctx context.Context, versionID, batchID, actor int64) (reportID int64, created bool, err error)
    RegenerateReport(ctx context.Context, versionID, reportID, actor int64) (int64, error)
}
type SupplementPort interface {
    StartSupplement(ctx context.Context, versionID, batchID, actor int64) (int64, error)
    GetSupplementRun(ctx context.Context, batchID int64) (*SupplementRunView, error)
    CreateSupplementBatch(ctx context.Context, versionID, parentBatchID int64, caseIDs []int64, actor int64) (*domain.RunBatch, error)
    MarkNodesMerged(ctx context.Context, versionID int64, caseIDs []int64, actor int64) error
}
type CodeImpactPort interface {
    StartImpact(ctx context.Context, versionID, baseCommit string) (int64, error)
    GetImpact(ctx context.Context, analysisID int64) (*ImpactView, error)
}
type VersionFinalizer interface {
    TryFinalize(ctx context.Context, versionID int64) (bool, error)
    SetContinuePhase(ctx context.Context, versionID int64, phase string) error
}

// mindmap
func (s *GraphService) AppendSupplementNode(ctx context.Context, versionID, actor int64, cmd SupplementNodeCommand) (domain.CaseNode, error)
func (s *GraphService) TransitionNodeStatus(ctx context.Context, versionID, nodeID, actor int64, to string) (domain.CaseNode, error)
func (s *GraphService) IncrementalPlan(ctx context.Context, versionID int64) (IncrementalPlanView, error)

// resource
type DBTargetService interface {
    Get(ctx context.Context, environmentID int64) (*domain.DBTarget, error) // nil,nil = 未配置
    Upsert(ctx context.Context, environmentID, actor int64, cmd UpsertDBTargetCommand) (domain.DBTarget, error)
    Delete(ctx context.Context, environmentID, actor int64) error
    Test(ctx context.Context, environmentID int64, override *UpsertDBTargetCommand) (domain.DBTargetTestResult, error)
}
type ReadOnlyQuerier interface {
    Query(ctx context.Context, target domain.DBTarget, sql string, limits QueryLimits) (domain.QueryResult, error)
}

// codeintel
type StartAnalysisInput struct { HeadCommit, BaseCommit string; Force bool }
func (s *AnalysisService) Start(ctx context.Context, versionID, actorID int64, in StartAnalysisInput) (domain.CodeAnalysis, bool, error)

// agent P1
type RunScope struct { VersionID, BatchID, SupplementRunID, AnalysisRunID int64 }
```

### 前端类型（T17–T20 产出一致命名）

- `ContinuePhase = ""|"clarifying"|"designing"|"executing"`；`BatchKind = "baseline"|"incremental"|"supplement"`；`CaseSource = "baseline"|"ai_supplement"`。
- API 客户端方法：`listIssues/getIssue/createIssue/patchIssue`、`listReports/ensureReport/getReport/regenerateReport/exportReport/diffReports`、`getIncrementalPlan/createIncrementalBatch`、`getDbTarget/upsertDbTarget/testDbTarget`。
- 所有 ID 保持 `string`；禁止 `Number()`。

---

## W1 — Schema 地基（T1）

### Task 1: P7 迁移 + order.txt + 验收尾部断言

**Wave 1 · 可并发：否（地基）**

**Files:**
- Create: `backend/internal/db/migrations/delivery_0001_issue.sql`
- Create: `backend/internal/db/migrations/delivery_0002_report.sql`
- Create: `backend/internal/db/migrations/delivery_0003_analysis.sql`
- Create: `backend/internal/db/migrations/resource_0007_db_target.sql`
- Create: `backend/internal/db/migrations/version_0005_continue.sql`
- Create: `backend/internal/db/migrations/execution_0002_autonomy.sql`
- Create: `backend/internal/db/migrations/mindmap_0002_supplement.sql`
- Create: `backend/internal/db/migrations/codeintel_0005_issue_anchor.sql`
- Modify: `backend/internal/db/migrations/order.txt`（末尾按上表追加）
- Modify: `scripts/acceptance.ps1:3182-3194`（P6 三迁移"恰在末尾"改为"包含且顺序正确"；保留 P6 存在性检查）
- Test: `backend/internal/db/migrate_p7_test.go`（新）

**Interfaces:**
- Consumes: 无
- Produces: spec §4.2 的全部表/列/约束/索引；`order.txt` 33–40 顺序。

- [ ] **Step 1: 写失败测试 `TestP7Schema`**（`backend/internal/db/migrate_p7_test.go`）：打开 `EZTEST_TEST_DATABASE_URL`，`Migrate` 两次；断言存在表 `issue/issue_ref/report/report_ref/analysis_run/db_target`，`version` 有 `continue_phase/continued_at`，`run_batch` 有 `kind/parent_batch_id/base_fallback_reason/graph_hash/supplement_attempted/autonomy_done`，`case_run` 有 `source`，`evidence` 的 kind 允许 `db_result`，`case_design_run` 的 kind 允许 `supplement/incremental_design`，`code_anchor` 的 subject_kind 允许 `issue`；再断言 `order.txt` 末尾 8 项顺序。
- [ ] **Step 2: 运行确认失败**：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/db -run TestP7Schema -v`；预期 FAIL（表/列不存在）。
- [ ] **Step 3: 按 spec §4.2 逐字实现 8 个迁移**；`order.txt` 末尾追加；`execution_0002` 的回填顺序必须为"加列→回填 graph_hash/autonomy_done→SET NOT NULL→加 CHECK"。
- [ ] **Step 4: 修改 `scripts/acceptance.ps1` P6 迁移断言**：仍检查三个 P6 文件存在且相对顺序正确，但允许其后有 P7 迁移（用 `IndexOf` 顺序比较，不再取 tail 相等）。
- [ ] **Step 5: 运行通过**：`scripts/test-integration.ps1 ./internal/db -run TestP7Schema -v`；再跑 `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly`，预期 EXIT=0（P7 API 检查尚未添加，不应引入红）。
- [ ] **Step 6: 提交（Lead）**：`git add backend/internal/db/migrations backend/internal/db/migrate_p7_test.go scripts/acceptance.ps1`；`git commit -m "feat(p7): add P7 schema migrations and migration gate"`。

---

## W2 — 核心领域/服务/仓储（T2–T11，可并行）

> 每个任务只写自己的 Files；共享的 `server.go`/`main.go` 放到 T16。所有任务只实现 application/domain/infrastructure（HTTP 留给 W3），除特别注明。

### Task 2: delivery issue 领域 + 服务 + 仓储

**Wave 2 · 可并发：是（与 T3–T11）**

**Files:**
- Create: `backend/internal/delivery/domain/issue.go`
- Create: `backend/internal/delivery/domain/errors.go`
- Create: `backend/internal/delivery/application/issue_service.go`
- Create: `backend/internal/delivery/application/issue_ports.go`
- Create: `backend/internal/delivery/infrastructure/postgres/issue_repository.go`
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: `platform/id`、`platform/errors`、`delivery/domain`、`IssueStore`（本任务定义）。
- Produces: `IssueService`（§Shared Interfaces）、`domain.Issue`、`domain.IssueRef`、`domain.IssueDraft`、`IssueFilter`、`CreateIssueCommand`；仓储接口 `IssueStore`。

- [ ] **Step 1: 写失败测试**：人工建单（source=human/dedupe null）；AI 单缺 dedupe/failureClass/caseNode → 拒绝；open→confirmed/dismissed；dismiss 无原因 → `issue_dismiss_reason_required`；expectedRevision 不符 → `issue_state_conflict`；同 dedupe open→追加 refs/复发+1；confirmed/dismissed 复发不改状态；跨版本 ref → `issue_ref_invalid`；列表筛选/分页。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 3: 实现**：`IssueService` 方法按 Shared Interfaces；`ApplyDrafts` 事务内去重/合并；`Confirm/Dismiss` CAS `revision`；`CreateHuman` 生成雪花 ID；事件/审计通过端口（本任务用 fake 端口测试，真实适配器在 T12/T16）。
- [ ] **Step 4: 跑通过**：同一命令；再 `..\.tools\go\bin\go.exe vet ./internal/delivery/...`
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/delivery`；`git commit -m "feat(p7): add delivery issue domain and service"`。

### Task 3: delivery report 领域 + 生成/差异/导出服务

**Wave 2 · 可并发：是（与 T2、T4–T11）**

**Files:**
- Create: `backend/internal/delivery/domain/report.go`、`report_content.go`、`report_diff.go`
- Create: `backend/internal/delivery/application/report_service.go`、`report_ports.go`、`report_markdown.go`
- Create: `backend/internal/delivery/infrastructure/postgres/report_repository.go`
- Create: `backend/internal/delivery/infrastructure/filestore/report_store.go`
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: `ExecutionReader`（批次/用例/证据计数）、`CaseGraphReader`（需求/用例/来源）、`IssueReader`、`VersionReader`、`ReportStore`、`ReportFileStore`、事件/审计端口（本任务定义）。
- Produces: `ReportService`（§Shared Interfaces）、`domain.Report/ReportContent/ReportDiff/ReportExport`、`ReportService.Diff` 语义。

- [ ] **Step 1: 写失败测试**：同输入两次生成 content 字节一致、排序稳定；`input_fingerprint` 稳定；无敏感字段；`Ensure` 首次创建 revision 1 published，重复返回已有；`Regenerate` revision+1；rerun 后指纹变化 → `RefreshStale` 置 stale 且只发一次事件；diff 分类（added/fixed/regressed/recovered/new/removed）；Markdown 含 Mermaid 与 DoD 字段；导出 sha256/size 一致。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 3: 实现**：`ReportContent` 结构按 spec §5.5；canonical JSON 排序；`source_hash` 指纹；`FileStore` 根内校验/临时文件+rename/0600；报告行 `content` 用 `json.RawMessage`；不读 execution 表（只经端口）。
- [ ] **Step 4: 跑通过**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/delivery`；`git commit -m "feat(p7): add delivery report generation, stale and diff"`。

### Task 4: delivery analysis_run 领域 + 服务 + worker

**Wave 2 · 可并发：是（与 T2、T3、T5–T11）**

**Files:**
- Create: `backend/internal/delivery/domain/analysis.go`、`analysis_validation.go`
- Create: `backend/internal/delivery/application/analysis_service.go`、`analysis_worker.go`、`analysis_ports.go`
- Create: `backend/internal/delivery/infrastructure/postgres/analysis_repository.go`
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: `AgentRuntime`、`ExecutionReader`、`IssueWriter`（= `IssueService.ApplyDrafts`）、`CodeAnchorPort`、事件/审计端口（本任务定义）。
- Produces: `AnalysisService`（§Shared Interfaces）、`AnalysisRun`、`AnalysisView`、`FailureCandidate`、`IssueDraft` 校验；worker 2s tick。

- [ ] **Step 1: 写失败测试**：failure 只接受 application 失败；候选缺证据/代码引用→warnings 不失败；triage done→ApplyDrafts；triage failed→failure 候选兜底建单；无 application 失败不启动；唯一 active；重启 queued/running→failed `stale_run_recovered`；LLM 不可用→degraded；结果 JSON 不含 prompt/证据正文。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 3: 实现**：`StartFailure/StartTriage` 建 run + 调 AgentRuntime（fake 测试）；`ProcessOnce` 轮询 run 终态并物化；`RecoverStale`；worker 2s/一次 5 个；所有 ID 字符串在 DTO（domain 内 int64）。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/delivery`；`git commit -m "feat(p7): add delivery failure analysis and triage runs"`。

### Task 5: resource db_target + 只读查询执行器 + HTTP

**Wave 2 · 可并发：是（与 T2–T4、T6–T11）**

**Files:**
- Create: `backend/internal/resource/domain/db_target.go`
- Create: `backend/internal/resource/application/db_target_service.go`、`db_query.go`、`db_target_ports.go`
- Create: `backend/internal/resource/infrastructure/postgres/db_target_repository.go`
- Create: `backend/internal/resource/infrastructure/pgreadonly/querier.go`
- Create: `backend/internal/resource/interfaces/httpapi/handlers_db_target.go`、`dto_db_target.go`
- Modify: `backend/internal/resource/interfaces/httpapi/routes.go`、`backend/internal/server/server.go`（挂 admin 路由；仅 T5 改）
- Test: 同目录 `*_test.go`、`backend/internal/resource/integration/db_target_test.go`

**Interfaces:**
- Consumes: `platform/id`、pgx pool、审计端口。
- Produces: `DBTargetService`、`ReadOnlyQuerier`、`domain.DBTarget/DBTargetTestResult/QueryResult/QueryLimits`；HTTP `GET/PUT/DELETE /api/resources/environments/{id}/db-target`、`POST .../test`。

- [ ] **Step 1: 写失败测试**：非 admin 403；密码不回显（JSON/详情/列表）；首次 password 必填、更新省略保留；字段范围校验；测试连接成功/失败/角色告警；SQL 白名单拒写/多语句/超长；行数/字节截断；连接/权限/SQL/只读错误映射；禁用/缺失。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/resource/...`
- [ ] **Step 3: 实现**：仓储明文密码（D48）；service 输出永不复制 password；`pgreadonly` 用 pgx `default_transaction_read_only=on` + `BEGIN READ ONLY` + `SET LOCAL statement_timeout` + 首关键字白名单 + 单语句；结果值规范化为字符串；测试连接 `SELECT version(), current_setting('transaction_read_only'), current_user` + 角色告警。
- [ ] **Step 4: 跑通过**：`..\.tools\go\bin\go.exe test -count=1 ./internal/resource/...`；集成：`scripts/test-integration.ps1 ./internal/resource -run TestDBTarget -v`。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/resource backend/internal/server/server.go`；`git commit -m "feat(p7): add environment db target and read-only query"`。

### Task 6: version continuing（触发/phase/finalize/DTO/changes）

**Wave 2 · 可并发：是（与 T2–T5、T7–T11）**

**Files:**
- Modify: `backend/internal/version/domain/version.go`（`ContinuePhase`、`ContinuedAt`、常量）
- Modify: `backend/internal/version/application/version_service.go`
- Modify: `backend/internal/version/application/requirement_service.go`（completed 建需求同事务触发）
- Modify: `backend/internal/version/infrastructure/postgres/version_repository.go`
- Modify: `backend/internal/version/interfaces/httpapi/dto.go`、`handlers_versions.go`（`continuePhase/continuedAt`、`/changes`）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: `platform/id`、事件/审计端口、版本仓储 tx。
- Produces: `MarkContinuing`、`SetContinuePhase`、`TryFinalize`、`CompleteContinuing`；`GET /versions/{id}` 新字段；`GET /versions/{id}/changes`。

- [ ] **Step 1: 写失败测试**：completed 建需求→continuing+clarifying+事件；archived→`version_not_continuable`；并发建需求只迁移一次；确认最后一条 draft→designing；建增量批后 executing；TryFinalize 四条件矩阵；paused 不 finalize；finalize 幂等；`/changes` 派生与排序。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/version/...`
- [ ] **Step 3: 实现**：`MarkContinuing` CAS `status='completed'`；requirement create 用同一 tx（若现有 service 无 tx，抽 `withTx`）；`TryFinalize` 查询批次/需求（经端口或同上下文仓储）并 CAS；`ContinuePhase` 只允许枚举；DTO 字符串/null。
- [ ] **Step 4: 跑通过**：`..\.tools\go\bin\go.exe test -count=1 ./internal/version/...`
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/version`；`git commit -m "feat(p7): add continuing lifecycle and requirement trigger"`。

### Task 7: execution 批次模型（kind/parent/graph_hash/source/门禁）

**Wave 2 · 可并发：是（与 T2–T6、T8–T11）**

**Files:**
- Modify: `backend/internal/execution/domain/types.go`、`entities.go`、`snapshot.go`
- Modify: `backend/internal/execution/application/batch_service.go`、`ports.go`
- Modify: `backend/internal/execution/infrastructure/postgres/batch_repository.go`、`case_run_repository.go`
- Modify: `backend/internal/execution/interfaces/httpapi/dto.go`、`handlers_batch.go`（DTO 加法字段；**路由不新增**）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: mindmap `ExecutionCaseProvider`（既有）、version 端口、事件/审计端口。
- Produces: `CreateBatchCommand`、`BatchKind`、`CaseSource`、`RunBatch` 新字段、`CaseRun.Source`、快照 `Origin`；`BatchService.Create` 新签名。

- [ ] **Step 1: 写失败测试**：baseline 门禁（含"所有终态批 autonomy_done"）；incremental 门禁（continuing/phase/当前图/base/head/history_rewritten）；supplement 门禁（parent 终态/自动创建）；`caseIds` 归属；`graph_hash` 稳定；snapshot `origin`/case_run.source；选中补全节点→merged；P6 baseline 行为回归。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...`
- [ ] **Step 3: 实现**：保留 P6 校验并扩展；`Create` 接收 `CreateBatchCommand`；graph hash 用现有快照排序/内容哈希；base/head 按 D49（含祖先校验，失败回退 `history_rewritten`）；批次仓储新增列；DTO 字符串 ID。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/execution`；`git commit -m "feat(p7): extend execution batches for incremental and supplement"`。

### Task 8: execution DB 断言判定/分类放开

**Wave 2 · 可并发：是（与 T2–T7、T9–T11）**

**Files:**
- Modify: `backend/internal/execution/domain/verdict.go`、`classification.go`
- Modify: `backend/internal/execution/application/case_runner.go`（reason 白名单）
- Test: `backend/internal/execution/domain/verdict_test.go`、`classification_test.go`、`backend/internal/execution/application/case_runner_test.go`

**Interfaces:**
- Consumes: 无（纯规则）
- Produces: `ValidateFinish` 支持 db 断言；`Classify` 映射 `db_connection_failed→environment`、`db_query_failed/db_readonly_violation/db_result_truncated/db_expected_invalid→case`、db 断言不匹配→application。

- [ ] **Step 1: 写失败测试**：含 db 断言可 passed/failed；db 未提交→`invalid_assertion_results`；db 未通过→passed 被拒；db 不匹配→failed(application)；写 SQL/截断→inconclusive(case)；连接失败→blocked(environment)；P6 非 db 矩阵回归。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/execution/domain/... ./internal/execution/application/...`
- [ ] **Step 3: 实现**：删除 `snapshotHasDBAssertion` 硬拒绝，改为全断言校验；分类白名单加 P7 reason；不改 P6 历史数据。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/execution`；`git commit -m "feat(p7): allow read-only db assertions in verdicts"`。

### Task 9: mindmap 补全写路径 + 补全 run + 增量计划

**Wave 2 · 可并发：是（与 T2–T8、T10、T11）**

**Files:**
- Create: `backend/internal/mindmap/application/graph_supplement.go`、`incremental_plan.go`
- Modify: `backend/internal/mindmap/domain/node.go`（状态常量/迁移表）、`backend/internal/mindmap/application/execution_service.go`（选择允许补全节点）、`backend/internal/mindmap/application/materialize.go`（supplement/incremental_design）、`backend/internal/mindmap/application/run_service.go`（kind/gate/promptVersion）
- Modify: `backend/internal/mindmap/infrastructure/postgres/*`（若需状态更新/查询）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: `GraphService` 现有 tx/事件/审计；`ExecutionCaseProvider` 现有；P4 impact read port（T10 定义）。
- Produces: `AppendSupplementNode`、`TransitionNodeStatus`、`IncrementalPlanView`、`SupplementNodeCommand`；run kinds `supplement/incremental_design`；物化规则。

- [ ] **Step 1: 写失败测试**：冻结后 Append 成功且 baseline 不变；冻结节点内容编辑仍 409；状态矩阵合法/非法；无需求来源拒绝；重复用例跳过；materialize 新节点 `supplemented`/`case_source`；高/中置信 affected→needs_revalidation、低不标；incremental-plan defaultCaseIds；run gate（executing/continuing+designing）。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/mindmap/...`
- [ ] **Step 3: 实现**：新方法只插入/只改状态；`beginWrite` 不用于新方法但事务内校验版本/基线；`TransitionNodeStatus` 检查矩阵；materialize 只接受合法提议；plan 读取 run.result + 节点。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/mindmap`；`git commit -m "feat(p7): add controlled supplement writes and incremental plan"`。

### Task 10: codeintel 增量 base + issue 锚点

**Wave 2 · 可并发：是（与 T2–T9、T11）**

**Files:**
- Modify: `backend/internal/codeintel/domain/anchor.go`（`issue`）
- Modify: `backend/internal/codeintel/application/anchor_service.go`
- Modify: `backend/internal/codeintel/application/analysis_service.go`（`StartAnalysisInput`）
- Modify: `backend/internal/codeintel/interfaces/httpapi/*`（DTO/handler baseCommit/subjectKind）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: 现有 analysis cache/unique（base/head 已存在）。
- Produces: `StartAnalysisInput`、`Start` 新签名；`subjectKind=issue` 创建/查询；`baseCommit` 校验/缓存。

- [ ] **Step 1: 写失败测试**：base 缺省=版本基线；显式 base 需 40-hex/祖先，否则 `invalid_base_commit`；同 base/head 复用，force 新版本；issue 锚点创建/查询/删除；跨版本 issue 拒绝。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/codeintel/...`
- [ ] **Step 3: 实现**：`Start` 签名改 `StartAnalysisInput`；更新所有调用点（本任务内）；`ValidateSubjectKind` 加 issue；API enum；缓存键含 base（表已支持）。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/codeintel`；`git commit -m "feat(p7): add incremental base and issue anchors to codeintel"`。

### Task 11: P1 RunScope 扩展（Version/Batch/Supplement/Analysis）

**Wave 2 · 可并发：是（与 T2–T10）**

**Files:**
- Modify: `backend/internal/agent/domain/run.go`（`RunScope` 新字段）
- Modify: `backend/internal/agent/application/tool_executor.go`（Invocation 透传）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: 现有 `StartWithScope`/`InvocationFromContext`。
- Produces: `RunScope{VersionID,BatchID,SupplementRunID,AnalysisRunID}`；工具侧可读。

- [ ] **Step 1: 写失败测试**：scope 四字段经 StartWithScope→tool executor→Invocation 完整透传；零值兼容 P1/P3/P5/P6 现有行为。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...`
- [ ] **Step 3: 实现**：只加字段/透传，不改现有字段语义；所有现有调用点编译通过。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/agent`；`git commit -m "feat(p7): extend agent run scope for delivery runs"`。

---

## W3 — HTTP / Agent / DB 工具 / 自主 worker（T12–T15，可并行）

### Task 12: delivery HTTP（issues/reports/analysis/global）

**Wave 3 · 可并发：是（与 T13–T15；仅改 delivery/interfaces 与 server 挂载）**

**Files:**
- Create: `backend/internal/delivery/interfaces/httpapi/handlers_issue.go`、`handlers_report.go`、`handlers_analysis.go`、`handlers_global.go`、`dto.go`、`routes.go`
- Modify: `backend/internal/server/server.go`（挂载 delivery 路由；仅本任务改 delivery 相关行）
- Test: `backend/internal/delivery/interfaces/httpapi/*_test.go`、`backend/internal/delivery/integration/http_test.go`

**Interfaces:**
- Consumes: T2/T3/T4 服务；P0 错误信封/分页/幂等/鉴权。
- Produces: spec §7.3–§7.5/§7.10 的端点；DTO 字段与 §7.11 reason。

- [ ] **Step 1: 写失败测试**：每条路由成功/401/403/404/409/422；ID 字符串；分页游标；issue 确认/驳回；report generate/publish/regenerate/export/diff；analysis 列表/手动 failure；全局列表深链字段；P6 报告端点不受影响。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 3: 实现**：handler 只调 application；DTO 转换；错误映射 reason；export 流式 + `X-Content-SHA256`；权限按 §11.1。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/delivery backend/internal/server/server.go`；`git commit -m "feat(p7): expose delivery HTTP API"`。

### Task 13: delivery Agent（failure/triage profile + tools + runtime）

**Wave 3 · 可并发：是（与 T12、T14、T15）**

**Files:**
- Create: `backend/internal/delivery/domain/profile.go`、`tools.go`（常量/种子）
- Create: `backend/internal/delivery/application/agent_tool/*.go`
- Create: `backend/internal/delivery/infrastructure/agent/runtime.go`
- Test: 同目录 `*_test.go`、`backend/internal/delivery/integration/agent_tools_test.go`

**Interfaces:**
- Consumes: T4 `AnalysisService`、P1 `StartWithScope`、T11 RunScope、T10 code read、execution reader。
- Produces: profiles `failure-analysis-agent`、`issue-triage-agent`；工具 `analysis.*`、`triage.*`；`NewTools` fail-closed。

- [ ] **Step 1: 写失败测试**：工具注册/白名单；scope 透传；候选/草稿 JSON 校验；越权/跨版本拒绝；无证据/代码引用 warnings；profile seed 幂等（`ON CONFLICT DO NOTHING`）。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/...`
- [ ] **Step 3: 实现**：工具薄壳调 `AnalysisService`/只读 ports；profile 常量（MaxTurns 20/600s/HardTimeout 120/Thinking disabled）；`infrastructure/agent` 适配 `StartWithScope`，`activity_channel=version:{id}`。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/delivery`；`git commit -m "feat(p7): add failure and triage agents"`。

### Task 14: supplement Agent + mindmap run HTTP/plan 端点

**Wave 3 · 可并发：是（与 T12、T13、T15）**

**Files:**
- Create: `backend/internal/mindmap/domain/profile_supplement.go`、`application/agent_tool/*.go`
- Create: `backend/internal/mindmap/infrastructure/agent/runtime_supplement.go`
- Create: `backend/internal/mindmap/interfaces/httpapi/handlers_supplement.go`、`dto_supplement.go`
- Modify: `backend/internal/mindmap/interfaces/httpapi/routes.go`
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: T9 GraphService/materialize、T11 RunScope、P4 impact reader、execution result reader。
- Produces: profile `supplement-agent`；工具 `supplement.*`；`POST /mindmap/runs` 新 kind；`GET /mindmap/incremental-plan`；`PATCH /mindmap/nodes/{id}/supplement`。

- [ ] **Step 1: 写失败测试**：supplement/incremental_design run 门禁；工具 JSON 校验/上限；plan 响应字段/defaultCaseIds/warnings；node supplement accept/reject；冻结内容不因 accept/reject 改变。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/mindmap/...`
- [ ] **Step 3: 实现**：工具累计到 run scope；materialize 已在 T9；HTTP 调用 application；profile seed 常量。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/mindmap`；`git commit -m "feat(p7): add supplement agent and incremental plan API"`。

### Task 15: execution db_assert 工具 + resource 适配 + 证据

**Wave 3 · 可并发：是（与 T12–T14）**

**Files:**
- Create: `backend/internal/execution/application/agent_tool/db_assert.go`
- Create: `backend/internal/execution/infrastructure/resource/db_query.go`（实现 execution 端口 → resource.ReadOnlyQuerier）
- Modify: `backend/internal/execution/application/agent_tool/tools.go`（注册/白名单校验）
- Modify: `backend/internal/execution/application/recorder.go`（写入 assertion_result db / 证据关联；若已有方法则复用）
- Modify: `backend/internal/execution/application/evidence_service.go`（允许 `db_result` kind）
- Test: 同目录 `*_test.go`、`backend/internal/execution/integration/db_assert_test.go`

**Interfaces:**
- Consumes: T5 `ReadOnlyQuerier`、T8 verdict、P6 recorder/evidence、T11 RunScope。
- Produces: 工具 `execution.db_assert`；`db_result` 证据；`assertion_result` 幂等写入；错误 reason 映射。

- [ ] **Step 1: 写失败测试**：非 db 断言/跨用例/重复调用；SQL 白名单/只读违规；连接失败→blocked 提示；期望比较（ordered/unordered/null/数字/时间）；截断→inconclusive；证据 sha256/归属；`case_finish` 校验集成。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...`
- [ ] **Step 3: 实现**：工具只接收 assertionId；从 snapshot 取 SQL/expected；调 resource 端口；写 evidence + assertion_result；返回预览（≤4KiB）；不泄露凭据/SQL 结果全文。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/execution`；`git commit -m "feat(p7): execute read-only db assertions"`。

### Task 16: 自主编排 worker + scheduler 改造

**Wave 3 · 可并发：是（与 T12–T15；仅改 execution/application/autonomy*/scheduler/ports）**

**Files:**
- Create: `backend/internal/execution/application/autonomy.go`、`autonomy_worker.go`、`autonomy_ports.go`
- Modify: `backend/internal/execution/application/scheduler.go`（移除无条件 CompleteExecution，改调 `AutonomyWorker.OnBatchTerminal`/`MarkDone`）
- Modify: `backend/internal/execution/application/ports.go`（新端口）
- Test: 同目录 `*_test.go`

**Interfaces:**
- Consumes: T2–T4 delivery 服务（端口）、T7 batch、T9 supplement、T6 version、T10 impact、T13/T14 run 状态。
- Produces: `AutonomyWorker`（`Start/Stop/RunOnce`）、批次链状态机、继续中增量设计段、finalize。

- [ ] **Step 1: 写失败测试**：无失败→报告；失败→分析→triage→报告；triage 失败兜底；报告后补全；0 新例/有子批/失败收尾；autonomy_done CAS/重入；暂停跳过；版本 finalize 条件；continuing designing→impact→incremental_design；不自动建增量批。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...`
- [ ] **Step 3: 实现**：2s tick、每 tick ≤`EZTEST_AUTONOMY_BATCHES_PER_TICK`；状态全由 DB 推导；所有启动失败 degrade；单实例；`OnBatchTerminal` 由 scheduler 调用。
- [ ] **Step 4: 跑通过**：同一命令。
- [ ] **Step 5: 提交（Lead）**：`git add backend/internal/execution`；`git commit -m "feat(p7): add autonomous orchestration worker"`。

---

## W4 — 组装根与启动 wiring（T17）

### Task 17: main.go 组装 + profile seed + worker 启停

**Wave 4 · 可并发：否（集成）**

**Files:**
- Modify: `backend/cmd/eztest/main.go`
- Modify: `backend/internal/server/server.go`（挂载 delivery / version changes / mindmap supplement / resource db-target 路由；T12 已挂 delivery 的，此处只补缺失组）
- Test: `backend/internal/integration/wiring_p7_test.go`（新，构建/health/profile seed 断言）

**Interfaces:**
- Consumes: T1–T16 所有服务/适配器/端口。
- Produces: 可启动 API；3 个 profile seed；`execution-agent` 的 `execution.db_assert` + prompt 追加；delivery analysis worker、execution autonomy worker、mindmap run worker 启停；优雅关闭。

- [ ] **Step 1: 写失败测试**（wiring）：`go build ./...` 通过后启动 API（test DB），断言：3 profile 存在、`execution-agent` allowed_tools 含 `execution.db_assert`、三个 worker 启停不 panic、health 200；缺失工具时启动失败信息含工具名。
- [ ] **Step 2: 跑失败**：`..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/integration -run TestP7Wiring -v`；预期 FAIL（未 wiring）。
- [ ] **Step 3: 实现**：按模块顺序构造 repositories/services/ports；`ensureP7Profiles` 在 builtin 之后 seed；`EnsureToolAllowed`/prompt 追加；`AutonomyWorker.Start`、`AnalysisService.Worker`、`Mindmap RunService.Worker` 启动；context cancel/Stop；所有路由挂载一次。
- [ ] **Step 4: 跑通过**：`..\.tools\go\bin\go.exe build ./...`；wiring test PASS；`scripts/dev-api.ps1` 手工启动 health 200（或脚本 smoke）。
- [ ] **Step 5: 提交（Lead）**：`git add backend/cmd/eztest backend/internal/server backend/internal/integration`；`git commit -m "feat(p7): wire delivery, autonomy and db target services"`。

---

## W5 — 前端交付（T18–T21，可并行）

### Task 18: 前端 delivery feature（问题/报告/全局组件）

**Wave 5a · 可并发：否（W5 先行；T19–T21 依赖本任务导出）**

**Files:**
- Create: `frontend/src/features/delivery/types.ts`、`api.ts`、`hooks/useIssues.ts`、`hooks/useReports.ts`
- Create: `frontend/src/features/delivery/components/IssuesTab.tsx`、`IssueFilters.tsx`、`IssueDetail.tsx`、`IssueDismissDialog.tsx`
- Create: `frontend/src/features/delivery/components/ReportsTab.tsx`、`ReportHeader.tsx`、`ReportDiff.tsx`、`ReportExportButton.tsx`
- Create: `frontend/src/features/delivery/components/GlobalIssuesPage.tsx`、`GlobalReportsPage.tsx`
- Test: 同目录 `*.test.tsx`、`hooks/*.test.ts`

**Interfaces:**
- Consumes: spec §7.3–§7.5 DTO；`src/lib/api-client.ts`、`sse-client.ts`（只经 lib）；`EvidenceViewer` 可从 execution feature 复用（只读导入）。
- Produces: `IssuesTab({versionId})`、`ReportsTab({versionId})`、`GlobalIssuesPage()`、`GlobalReportsPage()`；稳定 testid §10.9。

- [ ] **Step 1: 写失败测试**：列表筛选/分页；详情确认/驳回（原因必填，CAS 409 提示）；深链证据/用例/代码；空态/degraded；报告批次切换/revision/stale/regenerate/export/download；diff 选择 base 与计数；全局列表 version 筛选/深链；ID 全字符串。
- [ ] **Step 2: 跑失败**：`cd frontend; pnpm test -- --run src/features/delivery`
- [ ] **Step 3: 实现**：`api.ts` 只走 `api-client`；hook 合并 SSE 事件（issue.*/report.*）；无 feature 内 fetch/EventSource；组件拆分 <300 行/文件；错误/空态/加载。
- [ ] **Step 4: 跑通过**：`pnpm test -- --run src/features/delivery`；`pnpm typecheck`。
- [ ] **Step 5: 提交（Lead）**：`git add frontend/src/features/delivery`；`git commit -m "feat(p7): add issues and reports frontend feature"`。

### Task 19: versions feature（继续中/总览/Tab 挂载/增量计划）

**Wave 5b · 可并发：是（T18 完成后，与 T20、T21）**

**Files:**
- Modify: `frontend/src/features/versions/types.ts`（`continuePhase/continuedAt`、BatchKind）
- Modify: `frontend/src/features/versions/components/VersionHeader.tsx`、`VersionTabs.tsx`、`VersionDetailPage.tsx`、`OverviewTab.tsx`、`RequirementList.tsx`/`RequirementEditor.tsx`（completed 新增需求提示）
- Create: `frontend/src/features/versions/components/ContinuingBanner.tsx`、`IncrementalPlanPanel.tsx`
- Test: 同目录 `*.test.tsx`

**Interfaces:**
- Consumes: T18 `IssuesTab`/`ReportsTab`；T14 `GET /mindmap/incremental-plan`；T6 版本 DTO；T7 batch DTO。
- Produces: 继续中徽章/横幅/phase、增量计划面板、创建增量批按钮、总览指标/批次时间线；Tab 挂载真实问题/报告。

- [ ] **Step 1: 写失败测试**：completed 新增需求后徽章 continuing；phase 文案/横幅；无批次禁用暂停；增量计划默认勾选高/中、低置信不选、警告；创建批请求 body `kind=incremental,caseIds,parentBatchId`；总览卡/时间线；Tab 渲染 IssuesTab/ReportsTab；P6 旧空态断言更新。
- [ ] **Step 2: 跑失败**：`cd frontend; pnpm test -- --run src/features/versions`
- [ ] **Step 3: 实现**：类型字符串 ID；事件触发 refetch；错误/空态；不新增第 10 Tab；`VersionTabs` 只替换占位 slot。
- [ ] **Step 4: 跑通过**：`pnpm test -- --run src/features/versions`；`pnpm typecheck`。
- [ ] **Step 5: 提交（Lead）**：`git add frontend/src/features/versions`；`git commit -m "feat(p7): add continuing UI and real issue/report tabs"`。

### Task 20: execution feature（探索链路问题/批次 kind/db 证据/SSE）

**Wave 5b · 可并发：是（T18 完成后，与 T19、T21）**

**Files:**
- Modify: `frontend/src/features/execution/types.ts`、`api.ts`、`hooks/useExecution.ts`
- Modify: `frontend/src/features/execution/components/ExplorationTab.tsx`、`ChainGraph.tsx`、`chain-layout.ts`、`ResultsTab.tsx`、`EvidenceViewer.tsx`、`ReportTab.tsx`（保留 P6 Batch 1 入口/兼容）
- Test: 同目录 `*.test.tsx`、`hooks/*.test.ts`

**Interfaces:**
- Consumes: T18 API/types（问题）；T7 batch DTO；T15 db_result 证据；§8 事件。
- Produces: 问题节点/筛选、AI 补充/受影响样式、批次 kind 徽章、db 证据预览、P7 SSE 刷新。

- [ ] **Step 1: 写失败测试**：问题节点渲染/点击深链；AI 补充虚线/角标；needs_revalidation 黄边+文本；批次 kind 徽章；db_result 表格/截断/只读错误；SSE 8 类事件触发刷新；ID 字符串。
- [ ] **Step 2: 跑失败**：`cd frontend; pnpm test -- --run src/features/execution`
- [ ] **Step 3: 实现**：问题数据来自 delivery API；图节点 ≤200 折叠规则保留；不引入图库；EvidenceViewer `db_result` JSON 表格；P6 ReportTab 不删除，P7 版本级报告由 T18 接管。
- [ ] **Step 4: 跑通过**：`pnpm test -- --run src/features/execution`；`pnpm typecheck`。
- [ ] **Step 5: 提交（Lead）**：`git add frontend/src/features/execution`；`git commit -m "feat(p7): show issues, batch kinds and db evidence in execution UI"`。

### Task 21: resources DB 目标表单 + 全局路由/导航

**Wave 5b · 可并发：是（T18 完成后，与 T19、T20）**

**Files:**
- Create: `frontend/src/features/resources/components/DBTargetSection.tsx`、`hooks/useDBTarget.ts`
- Modify: `frontend/src/features/resources/*`（环境详情挂载；普通用户只读）
- Modify: `frontend/src/app/(app)/issues/page.tsx`、`frontend/src/app/(app)/reports/page.tsx`（挂 T18 全局组件）
- Modify: `frontend/src/navigation/nav-items.ts`（P7 阶段标签）
- Test: 同目录 `*.test.tsx`

**Interfaces:**
- Consumes: T5 DB target API；T18 `GlobalIssuesPage/GlobalReportsPage`；P2 资源页权限。
- Produces: DB 目标表单/测试连接/密码占位/告警；全局问题/报告路由；导航 phase 更新。

- [ ] **Step 1: 写失败测试**：admin 可见/普通用户只读；密码不回显；测试连接成功/失败/只读告警；保存/删除；全局页面渲染 + 深链；导航文案 P7。
- [ ] **Step 2: 跑失败**：`cd frontend; pnpm test -- --run src/features/resources src/app`
- [ ] **Step 3: 实现**：表单只发允许字段；password 留空=不发送；错误 reason 展示；app 页只组装。
- [ ] **Step 4: 跑通过**：`pnpm test -- --run src/features/resources src/app`；`pnpm typecheck`。
- [ ] **Step 5: 提交（Lead）**：`git add frontend/src/features/resources frontend/src/app frontend/src/navigation`；`git commit -m "feat(p7): add db target settings and global delivery pages"`。

---

## W6 — fake LLM / acceptance+OpenAPI / 文档（T22–T24，可并行）

### Task 22: fake LLM P7 模式 + 契约测试

**Wave 6a · 可并发：是（与 T24；仅 scripts/fake-llm*）**

**Files:**
- Modify: `scripts/fake-llm.cjs`
- Create: `scripts/fake-llm.p7.test.cjs`
- Test: `node scripts/fake-llm.p7.test.cjs`

**Interfaces:**
- Consumes: T13/T14 profile prompt markers/tool names；T15 db_assert；T16 worker 状态。
- Produces: `__p7-mode`（`normal|noop|degraded|invalid`）；确定性工具序列；契约测试。

- [ ] **Step 1: 写失败测试**：三个 marker 命中；每个 mode 返回结构；supplement 1–2 新用例+1 affected；failure 1–2 候选；triage 1 草稿；db 断言调用；degraded 直接结束；工具名/JSON 合法。
- [ ] **Step 2: 跑失败**：`node scripts/fake-llm.p7.test.cjs`；预期 FAIL。
- [ ] **Step 3: 实现**：按 P6 `planP6Turn` 模式新增 `planP7Turn`；模式读写端点；不破坏 P3–P6 既有 marker/mode。
- [ ] **Step 4: 跑通过**：`node scripts/fake-llm.p7.test.cjs`；`node scripts/fake-llm.p6.test.cjs`。
- [ ] **Step 5: 提交（Lead）**：`git add scripts/fake-llm.cjs scripts/fake-llm.p7.test.cjs`；`git commit -m "test(p7): add fake LLM P7 scenarios"`。

### Task 23: OpenAPI + acceptance P7 区块 + 静态门禁

**Wave 6b · 可并发：是（与 T22；T24 文档完成后）**

**Files:**
- Modify: `docs/openapi/api.yaml`
- Modify: `scripts/acceptance.ps1`（P7 区块 ≥20 检查；P6 迁移尾部已由 T1 改；事件/reason/testid/phase 检查；p7 fake 契约纳入）
- Modify: `scripts/test.ps1`（如需要，纳入 `fake-llm.p7.test.cjs`）
- Test: `scripts/acceptance.ps1 -StaticOnly`；`node scripts/fake-llm.p7.test.cjs`

**Interfaces:**
- Consumes: T5/T6/T7/T9/T12/T14/T15/T16 的真实端点/枚举。
- Produces: OpenAPI 契约；P7 静态验收；事件/reason 数量与 needle。

- [ ] **Step 1: 写失败检查**（先加检查再补契约）：P7 migration/deliverables、OpenAPI path/schema/event/reason、testid、导航 phase、fake 契约；运行 `scripts/acceptance.ps1 -StaticOnly` 预期 FAIL。
- [ ] **Step 2: 补 OpenAPI**：§7 全部路径/DTO；`VersionEventType` 8 个新事件；`ExecutionErrorReason` P7 reason；`code_anchor.subjectKind=issue`；P6 报告 DTO 不动。
- [ ] **Step 3: 补 acceptance**：≥20 检查（P7 migration、deliverables、issue/report/analysis/continuing/db-target/事件/理由/testid/OpenAPI、P6 回归、db-assertion 不再 SKIP）。静态可跳 API 依赖项并记 SKIP。
- [ ] **Step 4: 跑通过**：`scripts/acceptance.ps1 -StaticOnly` EXIT=0；`node scripts/fake-llm.p7.test.cjs`。
- [ ] **Step 5: 提交（Lead）**：`git add docs/openapi/api.yaml scripts/acceptance.ps1 scripts/test.ps1`；`git commit -m "test(p7): add OpenAPI contract and P7 acceptance checks"`。

### Task 24: 设计文档同步（D43–D52）

**Wave 6a · 可并发：是（与 T22；仅 docs/README/.env.example/Agent.md）**

**Files:**
- Modify: `docs/design/system-blueprint.md`、`implementation-phases.md`、`frontend-design.md`、`code-intelligence.md`、`auth-session-model.md`
- Modify: `Agent.md`（§6.1 目录树/上下文清单、§6.5、前端 feature）
- Modify: `README.md`（P7 章节/命令/限制）
- Modify: `.env.example`、`docs/dev-setup.local.md`（§16.1 新变量）
- Test: `git diff --check`；`grep` 校验 D43–D52/目录/命令存在（不修改 acceptance.ps1）。

**Interfaces:**
- Consumes: spec §14 清单。
- Produces: 文档与实现一致；主蓝图决策记录 D43–D52；README P7 命令。

- [ ] **Step 1: 写失败检查**：用 `git diff --check` + `Select-String` 检查 D43–D52、`delivery` 目录、P7 命令当前缺失（预期 FAIL/无命中）。
- [ ] **Step 2: 逐项更新文档**：按 spec §14；不改 P0–P8 编号；不记录进度；蓝图决策原文照抄 spec §2。
- [ ] **Step 3: 更新 README/.env.example/dev-setup**：命令、变量、已知限制。
- [ ] **Step 4: 跑通过**：`git diff --check`；`Select-String` 全部命中；`scripts/acceptance.ps1 -StaticOnly` EXIT=0（T23 尚未加 P7 文档 needle）。
- [ ] **Step 5: 提交（Lead）**：`git add docs Agent.md README.md .env.example`；`git commit -m "docs(p7): sync design documents and runbooks"`。

---

## W7 — E2E 与后端集成（T25–T26，可并行）

### Task 25: Playwright P7 E2E + helpers

**Wave 7 · 可并发：是（与 T26；仅 frontend/e2e + scripts/e2e.ps1）**

**Files:**
- Create: `frontend/e2e/p7-helpers.ts`、`p7-continuing.spec.ts`、`p7-issues-report.spec.ts`、`p7-supplement.spec.ts`、`p7-degraded.spec.ts`、`p7-db-config.spec.ts`
- Modify: `scripts/e2e.ps1`（纳入 P7 specs；如需环境变量/DB fixture）
- Test: `scripts/e2e.ps1 p7-continuing` 等

**Interfaces:**
- Consumes: T18–T21 前端；T22 fake LLM；T17 API；T5 DB target；P6 e2e helpers。
- Produces: 5 个 P7 spec；稳定 testid；截图/console error；DB fixture（如需要）。

- [ ] **Step 1: 写失败 spec**：continuing 全链；issues/report（确认/驳回/stale/regenerate/diff/export）；supplement 自动批/来源徽章；degraded 不崩；db config 表单/权限。先跑预期 FAIL。
- [ ] **Step 2: 实现 helpers**：seed 版本/需求/批次/失败结果；setP7Mode；wait 自主链；read events；DB target 配置。
- [ ] **Step 3: 跑通过**：`scripts/e2e.ps1 p7-continuing`、`p7-issues-report`、`p7-supplement`、`p7-degraded`、`p7-db-config` 全 PASS。
- [ ] **Step 4: 提交（Lead）**：`git add frontend/e2e scripts/e2e.ps1`；`git commit -m "test(p7): add Playwright E2E for autonomous loop"`。

### Task 26: 后端集成测试 + 跨上下文修复

**Wave 7 · 可并发：是（与 T25；后端测试/代码）**

**Files:**
- Create: `backend/internal/delivery/integration/*_test.go`
- Create: `backend/internal/execution/integration/autonomy_test.go`
- Create: `backend/internal/mindmap/integration/supplement_test.go`
- Modify: 必要的跨上下文端口/适配器（仅修复集成暴露的问题；不新增功能）
- Test: `scripts/test-integration.ps1 ./...`

**Interfaces:**
- Consumes: T1–T17。
- Produces: 真实 PG 下的迁移/仓储/worker/端口集成证据；修复。

- [ ] **Step 1: 写失败集成测试**：issue 并发去重；report 并发 revision；analysis/supplement 唯一 active；version 并发 continuing；autonomy 全链 fake 端口；DB target 真实连接/只读；文件存储 sha256。
- [ ] **Step 2: 跑失败**：`scripts/test-integration.ps1 ./...`。
- [ ] **Step 3: 修复**：只修集成暴露的问题；若需改接口，回到对应任务文件并说明（不静默改契约）。
- [ ] **Step 4: 跑通过**：`scripts/test-integration.ps1 ./...` 全绿。
- [ ] **Step 5: 提交（Lead）**：按修改文件 `git add ...`；`git commit -m "test(p7): add integration coverage and fix wiring"`。

---

## W8 — 全量门禁（T27）

### Task 27: 全量门禁 + 修复

**Wave 8 · 可并发：否**

**Files:** 按失败项定位；不新增功能。
**Test:** `scripts/test.ps1`、`scripts/acceptance.ps1`、`scripts/e2e.ps1`

- [ ] **Step 1: 跑 Go 单元/集成/前端**：`scripts/test.ps1`，记录失败。
- [ ] **Step 2: 跑总验收**：`scripts/acceptance.ps1`，记录 PASS/FAIL/SKIP。
- [ ] **Step 3: 跑 E2E**：`scripts/e2e.ps1`，记录。
- [ ] **Step 4: 修复所有 P7 引入的 FAIL**；P6 回归必须绿；SKIP 只能为 gated/真实验收项。
- [ ] **Step 5: 重跑三命令**：`EXIT=0`；提交（Lead）：`git add ...`；`git commit -m "test(p7): make full gates green"`。

---

## W9 — 真实验收脚本（T28–T29，可并行）

### Task 28: 真实 LLM 验收脚本

**Wave 9 · 可并发：是（与 T29；仅 scripts）**

**Files:**
- Create: `scripts/acceptance-p7-llm.ps1`
- Modify: `scripts/fixture-codeintel-repo.ps1`（如需 incremental commit B）或新增 fixture 辅助
- Test: 脚本 `-WhatIf`/`-StaticOnly`（若实现）；`EZTEST_RUN_REAL_LLM=1` 正式运行放到 T30

**Interfaces:**
- Consumes: spec §12.7 场景 1–4；真实 `.env`；executor/fixture/API/Web。
- Produces: gated 脚本；token/耗时统计；证据目录 `.tmp/p7-real-llm/`。

- [ ] **Step 1: 写脚本骨架 + 静态自检**：检查 `EZTEST_RUN_REAL_LLM=1`、LLM 配置存在（不打印）、executor health、fixture health；场景函数化。
- [ ] **Step 2: 实现场景 1–4**：补全/失败分析+问题/继续中+commit B/降级；断言事件、DB、报告、节点来源；失败即非零退出。
- [ ] **Step 3: 干跑静态检查**：`$env:EZTEST_RUN_REAL_LLM="0"; scripts/acceptance-p7-llm.ps1` 应快速跳过并 EXIT=0（或明确提示 gated）。
- [ ] **Step 4: 提交（Lead）**：`git add scripts/acceptance-p7-llm.ps1 scripts/fixture-codeintel-repo.ps1`；`git commit -m "test(p7): add real LLM acceptance script"`。

### Task 29: 真实 DB 验收脚本

**Wave 9 · 可并发：是（与 T28；仅 scripts）**

**Files:**
- Create: `scripts/acceptance-p7-db.ps1`
- Create: `scripts/fixture-db.ps1`（目标库/只读角色/fixture 表）
- Test: `scripts/fixture-db.ps1 -WhatIf`；正式运行 T31

**Interfaces:**
- Consumes: spec §12.8/§16.4；本机 PG `:5433`；admin API。
- Produces: 幂等目标库/角色/数据；DB 断言 passed/failed/写拒绝/错密/截断/P6 重跑/权限/泄漏 grep。

- [ ] **Step 1: 写 fixture 脚本**：创建 `eztest_p7_target`、`eztest_p7_ro`、表/种子（幂等）；`-WhatIf` 输出计划。
- [ ] **Step 2: 写验收脚本**：8 项检查；任何失败非零退出；证据 `.tmp/p7-real-db/`。
- [ ] **Step 3: 干跑**：`scripts/fixture-db.ps1 -WhatIf`、`scripts/acceptance-p7-db.ps1 -WhatIf`。
- [ ] **Step 4: 提交（Lead）**：`git add scripts/acceptance-p7-db.ps1 scripts/fixture-db.ps1`；`git commit -m "test(p7): add real database acceptance script"`。

---

## W10 — 真实验收运行（T30–T31，串行）

### Task 30: 真实 LLM 验收运行 + 修复

**Wave 10 · 可并发：否（共享环境）**

**Files:** 失败修复；证据 `.tmp/p7-real-llm/`。
**Test:** `$env:EZTEST_RUN_REAL_LLM="1"; scripts/acceptance-p7-llm.ps1`

- [ ] **Step 1: 启动 API/Web/executor/fixture**（脚本内或手工），确认健康。
- [ ] **Step 2: 运行真实 LLM 脚本**；记录耗时/token/场景结果。
- [ ] **Step 3: 修复失败**（只修 P7 缺陷；若发现 spec 冲突，记录 ruling 到 ledger）。
- [ ] **Step 4: 重跑至全绿**；证据归档。
- [ ] **Step 5: 提交（Lead）**：`git add ...`；`git commit -m "fix(p7): harden real LLM acceptance path"`。

### Task 31: 真实 DB 验收运行 + 修复

**Wave 10 · 可并发：否（共享环境）**

**Files:** 失败修复；证据 `.tmp/p7-real-db/`。
**Test:** `scripts/acceptance-p7-db.ps1`

- [ ] **Step 1: 运行 fixture + DB 验收脚本**。
- [ ] **Step 2: 核对 8 项检查 + 泄漏 grep**。
- [ ] **Step 3: 修复失败**；重跑至全绿。
- [ ] **Step 4: 证据归档**（DB 行、断言结果、证据 sha、报告）。
- [ ] **Step 5: 提交（Lead）**：`git add ...`；`git commit -m "fix(p7): harden real database acceptance path"`。

---

## W11 — 真实浏览器人工验收（T32）

### Task 32: Lead 代用户执行真实人工验收

**Wave 11 · 可并发：否**

**Files:**
- Create: `scripts/acceptance-p7-manual.ps1`（真实浏览器逐步操作 + 截图；或用 Playwright spec + 人工脚本）
- Create: `.superpowers/sdd/2026-10-28-eztest-p7-autonomous-loop/acceptance.md`（gitignored）
- Evidence: `.tmp/p7-manual/` 截图/console/network/DB/事件/报告

**Interfaces:**
- Consumes: spec §12.9 步骤；真实 LLM/DB/浏览器。
- Produces: 验收记录 + DoD 映射 + 遗留清单。

- [ ] **Step 1: 写人工验收脚本**：登录→continuing→增量计划→建批→执行→问题→报告→差异→导出→DB 配置/测试→总览/探索链路→降级；每步截图；console/network error 收集。
- [ ] **Step 2: 运行脚本**；失败即停下修复并重跑受影响步骤。
- [ ] **Step 3: 核对无假数据/无凭据泄漏/深链/状态一致**。
- [ ] **Step 4: 写 acceptance.md**：DoD 五条映射、§2.9 锚点、证据路径、已知限制、P8 遗留。
- [ ] **Step 5: 提交（Lead）**：`git add scripts/acceptance-p7-manual.ps1`；`git commit -m "test(p7): add manual browser acceptance walkthrough"`。

---

## W12 — 最终 review 与合并（T33）

### Task 33: 整支 review + 修复 + 合回 dev

**Wave 12 · 可并发：否**

**Files:** 按 review 发现修复。
**Test:** 最终全量门禁 + 真实脚本报告。

- [ ] **Step 1: 生成整支 diff 包**（base=spec 提交前的 dev HEAD `4963f91`；head=当前）并交给最终 reviewer（fresh context，capable model）。
- [ ] **Step 2: 修复 Critical/Important；Minor 记录 ledger/报告。**
- [ ] **Step 3: 重跑 `scripts/test.ps1`、`scripts/acceptance.ps1`、`scripts/e2e.ps1`、真实 LLM/DB 脚本（或最近一次全绿证据 + 修复后回归子集）。**
- [ ] **Step 4: 写完成报告**：DoD/锚点/证据/Rulings/P8 遗留。
- [ ] **Step 5: 合回 dev**：`git switch dev; git merge --ff-only feat/p7-autonomous-loop`（仅本地，不 push）；记录 merge commit/HEAD。
- [ ] **Step 6: 清理 SDD workspace**（保留 acceptance.md? 按 P6 惯例可留档；`.superpowers` gitignored）。最终回复用户。

---

## Self-Review（写完计划后自查）

**1. Spec coverage**

| spec 章节 | 任务 |
|---|---|
| §4 数据模型/迁移 | T1 |
| §5.1 Version | T6 |
| §5.2 CaseNode | T9 |
| §5.3 RunBatch/CaseRun | T7 |
| §5.4 Issue | T2/T12 |
| §5.5 Report | T3/T12 |
| §5.6 AnalysisRun | T4/T12 |
| §5.7 DbTarget | T5 |
| §5.8 覆盖/受影响 | T9/T14/T19 |
| §6.1 continuing | T6/T9/T14/T19 |
| §6.2 自主 worker | T16 |
| §6.3 失败→问题→报告 | T4/T13/T16 |
| §6.4 补全运行/物化 | T9/T14/T16 |
| §6.5 报告 stale/diff/export | T3/T12/T16 |
| §6.6 DB 断言 | T8/T15 |
| §6.7 暂停/重启/降级 | T16/T25 |
| §6.8 base/head | T7/T10 |
| §7 API | T12/T14/T5/T6/T7/T23 |
| §8 事件 | T16/T23 |
| §9 Agent | T13/T14/T15/T17 |
| §10 前端 | T18–T21 |
| §11 权限/审计/安全 | T2–T5/T12/T23 |
| §12 测试 | T22/T25/T26/T27/T28/T29/T30/T31/T32 |
| §13 验收 | T30–T32/T33 |
| §14 文档 | T24 |
| §15 风险 | 各任务 Review Focus + T33 |
| §16 附录 | T1/T5/T23/T24/T28/T29 |

**2. Step scan**：每个任务都有"失败测试→跑失败→实现→跑通过→Lead 提交"；未出现"处理边界/TBD"空步骤。接口名在 Shared Interfaces 与任务间一致；`CreateBatchCommand`、`MarkContinuing`、`TryFinalize`、`AppendSupplementNode`、`DBTargetService`、`StartAnalysisInput` 等均只定义一次。

**3. Type consistency**：`IssueDraft` 在 T2/T4/T13 共用；`ReportService.Ensure` 返回 `(Report,bool,error)`；`AnalysisService.StartFailure` 返回 `(AnalysisRun,error)` 而 `AnalysisPort.StartFailureAnalysis` 返回 `(int64,error)`（跨上下文适配器负责转换，wiring 在 T17）；`CreateBatchCommand.Kind` 与 `domain.BatchKind` 字符串常量一致；前端 `ContinuePhase/BatchKind/CaseSource` 与后端枚举一致。

**4. Review Focus 测试归属**：冻结基线→T9；批次/版本完成时序→T7/T16；DB 只读/凭据→T5/T15/T23/T25；报告确定性/stale→T3/T23/T25；AI 降级→T4/T16/T22/T25。

**5. Proportion**：计划按任务列接口/测试名/关键断言，未粘贴实现体；长文件已拆分（delivery report/issue/analysis、mindmap supplement、execution autonomy）。T1/T7/T9/T16 是最大任务，已分别限制在单上下文内；若实际超出，可在执行时按 ledger ruling 拆成子任务，但不得改变 Shared Interfaces。

---

*Plan v1.0（2026-10-28）。执行方式：Subagent-Driven Development + 并发波次（用户要求"标记可并发的任务并发执行"）；Lead 统一提交与验收。*

